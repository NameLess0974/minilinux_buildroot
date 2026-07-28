#!/bin/bash
# Auto-installer RPI OS pour Buildroot HTTP Boot
# Telecharge et flash RPI OS sur la carte SD
# Version streaming avec verification signature RSA

# Configuration
# Serveur : deux canaux. HTTP (18743) ne sert QUE boot.img/boot.sig au firmware
# EEPROM (qui ne fait pas de TLS). Tout ce que ce script fait passe en HTTPS
# (18443) avec pinning du certificat (--cacert), cf. guide serveur.
SERVER="bootloader.sabsystem.com"
HTTPS_BASE="https://${SERVER}:18443"
CACERT="/etc/minilinux/server.crt"          # cert public pinne (overlay)

BASE_URL="${HTTPS_BASE}/images"
IMAGE_URL="${BASE_URL}/final_image.img.xz"
SIG_URL="${BASE_URL}/final_image.sig"
HEALTH_URL="${HTTPS_BASE}/health"           # sonde de disponibilite avant le flux
TARGET_DEVICE="/dev/mmcblk0"
PUBLIC_KEY="/etc/keys/bootkey-public.pem"
MAX_RETRIES=3
RETRY_DELAY=30
PROGRESS_FILE="/tmp/progress"
INSTALL_LOG="/tmp/install.log"              # journal complet envoye avant reboot

# --- Robustesse TLS/reseau ---
# Au demarrage, le reseau n'est pas toujours stable (juste apres le gros boot.img
# HTTP) : la 1ere poignee de main HTTPS peut echouer en "bad record MAC" (erreur
# TRANSPORT, pas HTTP). --retry-all-errors est la cle : il fait reessayer curl sur
# ces erreurs transport, la ou --retry seul ne couvre que les 5xx/transitoires HTTP.
# Ces retries sont INTERNES a curl -> un echec transitoire ne gache pas une des 3
# tentatives d'installation.
#
# CURL_RETRY_DL  : telechargements critiques (image, signature) -> patient (5 x 2s).
# CURL_RETRY_TEL : telemetrie best-effort -> leger (2 x 1s) pour ne jamais bloquer
#                  la progression si le serveur est lent/injoignable.
CURL_RETRY_DL="--retry 5 --retry-delay 2 --retry-all-errors --retry-connrefused"
CURL_RETRY_TEL="--retry 2 --retry-delay 1 --retry-all-errors"

# Identite machine + session (calcules une fois, cf. get_mac_address plus bas).
# BOOT_ID = MAC + uptime : stable pour toute la session, survit au sens "groupe".
MAC=""
BOOT_ID=""
ATTEMPT=1
# Codes de sortie du dernier pipeline de streaming (remplis par stream_and_flash,
# envoyes en telemetrie sur echec pour identifier le maillon fautif).
STREAM_CURL_EXIT=0
STREAM_XZ_EXIT=0
STREAM_DD_EXIT=0

# === LOGGING ===
# Chaque ligne va sur /dev/kmsg, stdout, ET dans $INSTALL_LOG (envoye au serveur
# avant reboot via send_logs).
log() {
    local msg="[RPI-INSTALLER] $*"
    echo "<6>${msg}" > /dev/kmsg 2>/dev/null
    echo "$msg"
    echo "$msg" >> "$INSTALL_LOG" 2>/dev/null || true
}

log_error() {
    local msg="[RPI-INSTALLER] ERROR: $*"
    echo "<3>${msg}" > /dev/kmsg 2>/dev/null
    echo "$msg" >&2
    echo "$msg" >> "$INSTALL_LOG" 2>/dev/null || true
}

log_warn() {
    local msg="[RPI-INSTALLER] WARN: $*"
    echo "<4>${msg}" > /dev/kmsg 2>/dev/null
    echo "$msg"
    echo "$msg" >> "$INSTALL_LOG" 2>/dev/null || true
}

log_section() {
    log "=================================================="
    log "$*"
    log "=================================================="
}

# Ecrire le pourcentage d'avancement dans le fichier de progression
# Lu par fb_video (fb_video.c) pour mettre a jour l'affichage framebuffer
set_progress() {
    echo "$1" > "${PROGRESS_FILE}"
}

# === FONCTIONS UTILITAIRES ===


wait_for_device() {
    local device="$1"
    local timeout="${2:-30}"

    log "Attente device ${device} (timeout: ${timeout}s)..."

    while [ ! -b "${device}" ]; do
        sleep 1
        timeout=$((timeout - 1))
        if [ $timeout -le 0 ]; then
            log_error "Device ${device} introuvable!"
            return 1
        fi
    done

    log "Device ${device} disponible"
    return 0
}

check_network() {
    log "Verification reseau..."

    local ip_info
    ip_info=$(ip -4 addr show scope global 2>/dev/null | grep inet | head -1)
    [ -n "$ip_info" ] && log "IP: $ip_info"

    if ping -c 1 -W 3 bootloader.sabsystem.com >/dev/null 2>&1; then
        log "Connectivite serveur: OK"
        return 0
    fi

    if ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1; then
        log "Connectivite Internet: OK"
        return 0
    fi

    log_error "Pas de connectivite!"
    return 1
}

# Attend que le serveur reponde en HTTPS (poignee de main TLS OK) avant de lancer
# le flux. Evite le "bad record MAC" du 1er appel : on ne demarre les vrais
# telechargements qu'une fois le canal TLS reellement etabli. Best-effort borne :
# si /health ne repond jamais, on continue quand meme (les curl ont leur propre
# retry) au bout de ~30s.
wait_server_ready() {
    local max_wait="${1:-30}" waited=0
    log "Attente disponibilite serveur HTTPS (${HEALTH_URL})..."
    while [ $waited -lt $max_wait ]; do
        if curl --cacert "$CACERT" -sf --connect-timeout 3 --max-time 5 \
                "$HEALTH_URL" >/dev/null 2>&1; then
            log "Serveur HTTPS pret (apres ${waited}s)"
            return 0
        fi
        sleep 2
        waited=$((waited + 2))
    done
    log_warn "Serveur HTTPS non confirme apres ${max_wait}s (on continue, curl retentera)"
    return 1
}

safe_release_device() {
    local device="$1"

    log "Liberation device ${device}..."
    sync

    for part in ${device}p* ${device}; do
        umount -l "$part" 2>/dev/null || true
    done

    sleep 1
    sync

    local procs
    procs=$(fuser "${device}"* 2>/dev/null | tr -s ' ' '\n' | sort -u | tr '\n' ' ')
    if [ -n "$procs" ]; then
        log_warn "Process utilisant ${device}: $procs"
        for pid in $procs; do
            [ "$pid" = "$$" ] && continue
            [ "$pid" = "$PPID" ] && continue
            [ "$pid" = "1" ] && continue
            if [ -d "/proc/$pid" ]; then
                kill -TERM "$pid" 2>/dev/null
                sleep 0.5
                kill -KILL "$pid" 2>/dev/null || true
            fi
        done
    fi

    blockdev --rereadpt "${device}" 2>/dev/null || true
    sync
    sleep 1
    log "Device ${device} libere"
}

wipe_device() {
    local device="$1"

    log_section "WIPE DEVICE ${device}"
    log "Effacement des 100 premiers MB..."

    dd if=/dev/zero of="${device}" bs=1M count=100 conv=fsync 2>/dev/null
    sync

    log "Device ${device} efface"
}

# === STREAMING AVEC HASH ===

stream_and_flash() {
    local url="$1"
    local device="$2"
    local hash_file="/tmp/image_hash.txt"

    log_section "STREAMING + FLASH"
    log "URL: $url"
    log "Device: $device"
    log "Pipeline: curl -> tee(sha256sum) -> xz -d -> dd"

    # Cleanup
    rm -f "$hash_file"

    # Creer fichiers pour capturer stderr
    local curl_err="/tmp/curl_err.log"
    local xz_err="/tmp/xz_err.log"
    local dd_err="/tmp/dd_err.log"
    rm -f "$curl_err" "$xz_err" "$dd_err"

    # --- MONITOR DE PROGRESSION TIME-BASED (25% -> 65%) ---
    # +1% toutes les 15s, bloque a 65% jusqu'a la fin du pipeline
    # Un seul processus background, aucun impact sur la pipeline
    (
        pct=25
        while [ "${pct}" -lt 65 ]; do
            sleep 15
            pct=$(( pct + 1 ))
            echo "${pct}" > "${PROGRESS_FILE}"
        done
        # Bloque ici a 65% jusqu'a ce qu'on soit tue (pipeline terminee)
        while true; do sleep 60; done
    ) &
    local flash_monitor_pid=$!
    # -------------------------------------------------------

    log "Lancement pipeline streaming..."

    # Pipeline avec tee pour calculer le hash du fichier compresse
    # Utiliser named pipe pour garantir que sha256sum lit tout
    local fifo="/tmp/hash_fifo.$$"
    rm -f "$fifo"                    # nettoyer un eventuel reliquat d'un run tue
    if ! mkfifo "$fifo"; then
        log_error "Impossible de creer le FIFO $fifo"
        kill "${flash_monitor_pid}" 2>/dev/null
        wait "${flash_monitor_pid}" 2>/dev/null
        return 1
    fi

    # Lancer sha256sum en background
    (sha256sum < "$fifo" | awk '{print $1}' > "$hash_file") &
    local sha_pid=$!

    # Pipeline principal (curl HTTPS pinne -> tee -> xz -> dd).
    # VOLONTAIREMENT SANS --retry : sur un flux pipe (pas -o fichier), curl ne peut
    # pas reprendre ; un retry apres coupure en cours re-telechargerait tout depuis
    # 0 -> flux .xz partiel + complet concatenes = CORROMPU. Toute coupure fait
    # echouer proprement le pipeline -> la boucle MAX_RETRIES relance une install
    # PROPRE depuis le debut. La stabilite initiale du reseau est deja assuree par
    # wait_server_ready (/health) appele avant d'arriver ici.
    # --no-progress-meter : supprime la barre de progression curl (des centaines de
    # lignes) qui polluait les logs ET gonflait le blob envoye au serveur. On garde
    # -S pour n'afficher que les vraies erreurs. La progression visuelle reste
    # assuree par le monitor time-based -> /tmp/progress (splash).
    curl --cacert "$CACERT" -f -L -S --no-progress-meter \
        --connect-timeout 30 \
        --max-time 1800 \
        "$url" 2>"$curl_err" | \
    tee "$fifo" | \
    xz -d 2>"$xz_err" | \
    dd of="$device" bs=4M conv=fsync oflag=direct 2>"$dd_err"

    local pipe_status=("${PIPESTATUS[@]}")
    local curl_exit=${pipe_status[0]}
    local tee_exit=${pipe_status[1]}
    local xz_exit=${pipe_status[2]}
    local dd_exit=${pipe_status[3]}
    # Exposer les codes pour la telemetrie (diagnostic du maillon fautif, cf. spec).
    STREAM_CURL_EXIT=$curl_exit
    STREAM_XZ_EXIT=$xz_exit
    STREAM_DD_EXIT=$dd_exit

    # Pipeline terminee - arreter le monitor time-based
    kill "${flash_monitor_pid}" 2>/dev/null
    wait "${flash_monitor_pid}" 2>/dev/null

    # Attendre que sha256sum finisse
    wait $sha_pid
    rm -f "$fifo"

    log "Pipeline termine"
    log "Exit codes - curl: $curl_exit | tee: $tee_exit | xz: $xz_exit | dd: $dd_exit"

    # Afficher les erreurs/infos
    [ -s "$curl_err" ] && log "curl stderr: $(head -5 $curl_err)"
    [ -s "$xz_err" ] && log "xz stderr: $(cat $xz_err)"
    [ -s "$dd_err" ] && log "dd info: $(cat $dd_err)"

    # Cleanup logs
    rm -f "$curl_err" "$xz_err" "$dd_err"

    # Verifier succes du pipeline
    if [ $curl_exit -eq 0 ] && [ $xz_exit -eq 0 ] && [ $dd_exit -eq 0 ]; then
        # Attendre que le hash soit ecrit (tee process substitution peut etre lent)
        local wait_count=0
        while [ ! -s "$hash_file" ] && [ $wait_count -lt 10 ]; do
            sleep 1
            wait_count=$((wait_count + 1))
        done

        if [ -s "$hash_file" ]; then
            log "Streaming: SUCCES"
            log "Hash calcule: $(cat $hash_file)"
            return 0
        else
            log_error "Hash non calcule!"
            return 1
        fi
    else
        log_error "Streaming: ECHEC"
        [ $curl_exit -ne 0 ] && log_error "curl a echoue (code $curl_exit)"
        [ $xz_exit -ne 0 ] && log_error "xz a echoue (code $xz_exit)"
        [ $dd_exit -ne 0 ] && log_error "dd a echoue (code $dd_exit)"
        return 1
    fi
}

set_boot_signature() {
    local device="$1"
    local state="$2"
    local bytes

    if [ "$state" = "on" ]; then
        bytes='\x55\xaa'
    else
        bytes='\x00\x00'
    fi

    if printf "$bytes" | dd of="${device}" bs=1 seek=510 conv=notrunc status=none 2>/dev/null; then
        sync
        log "Signature de boot MBR: ${state}"
        return 0
    fi

    log_error "Impossible d'ecrire la signature de boot MBR (${state})"
    return 1
}

verify_signature() {
    local sig_file="$1"
    local hash_file="$2"
    local pubkey="$3"

    log_section "VERIFICATION SIGNATURE RSA"

    if [ ! -f "$pubkey" ]; then
        log_error "Cle publique non trouvee: $pubkey"
        return 1
    fi

    if [ ! -f "$sig_file" ]; then
        log_error "Fichier signature non trouve: $sig_file"
        return 1
    fi

    if [ ! -f "$hash_file" ]; then
        log_error "Fichier hash non trouve: $hash_file"
        return 1
    fi

    local hash
    hash=$(cat "$hash_file")
    log "Hash image: $hash"
    log "Cle publique: $pubkey"
    log "Signature: $sig_file"

    # Creer fichier temporaire avec le hash au format attendu
    local hash_data="/tmp/hash_data.txt"
    echo "$hash" > "$hash_data"

    # Verifier la signature RSA
    if openssl dgst -sha256 -verify "$pubkey" -signature "$sig_file" "$hash_data" 2>/dev/null; then
        log "Signature: VALIDE"
        rm -f "$hash_data"
        return 0
    else
        log_error "Signature: INVALIDE"
        rm -f "$hash_data"
        return 1
    fi
}

download_signature() {
    local url="$1"
    local output="$2"

    log "Telechargement signature: $url"

    # HTTPS avec pinning du cert (--cacert). Pas de fallback wget : il ne connait
    # pas notre CA auto-signee et casserait le pinning.
    if curl --cacert "$CACERT" $CURL_RETRY_DL -f -L -S --connect-timeout 30 -o "$output" "$url" 2>/dev/null; then
        log "Signature telechargee: $(ls -la $output)"
        return 0
    else
        log_error "Impossible de telecharger la signature"
        return 1
    fi
}


# === CONFIRMATION AU SERVEUR ===

get_mac_address() {
    # Recuperer la MAC de l'interface principale
    local mac
    mac=$(ip link show eth0 2>/dev/null | grep ether | awk '{print $2}' | tr '[:lower:]' '[:upper:]')
    if [ -z "$mac" ]; then
        mac=$(ip link 2>/dev/null | grep ether | head -1 | awk '{print $2}' | tr '[:lower:]' '[:upper:]')
    fi
    echo "$mac"
}

# Echappe une chaine pour l'inserer dans une valeur JSON (guillemets, backslash).
json_escape() {
    # backslash d'abord, puis guillemets ; supprime retours a la ligne
    printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g' | tr -d '\r\n'
}

# Initialise MAC + BOOT_ID (une seule fois, en debut de main).
init_identity() {
    MAC=$(get_mac_address)
    if [ -z "$MAC" ]; then
        log_warn "MAC introuvable"
        MAC="UNKNOWN"
    fi
    # uptime entier (secondes) : stable pour toute la session
    local up
    up=$(cut -d. -f1 /proc/uptime 2>/dev/null)
    BOOT_ID="${MAC}-${up:-0}"
    log "Identite: mac=${MAC} boot_id=${BOOT_ID}"
}

# Telemetrie de progression. Best-effort : jamais bloquant, jamais fatal.
# Usage : send_event <step> <status> <progress> <message> [details_json]
send_event() {
    local step="$1" status="$2" progress="$3" message="$4"
    # NB : ne PAS ecrire "${5:-{}}" -> bash y laisse un '}' parasite (JSON casse).
    local details="$5"
    [ -z "$details" ] && details="{}"
    local msg_esc
    msg_esc=$(json_escape "$message")
    curl --cacert "$CACERT" $CURL_RETRY_TEL --max-time 8 --silent --output /dev/null \
        -X POST "${HTTPS_BASE}/api/v1/events" \
        -H "Content-Type: application/json" \
        -d "{\"mac\":\"${MAC}\",\"boot_id\":\"${BOOT_ID}\",\"ts\":$(date +%s),\"step\":\"${step}\",\"status\":\"${status}\",\"progress\":${progress},\"attempt\":${ATTEMPT},\"message\":\"${msg_esc}\",\"details\":${details}}" \
        2>/dev/null || true
}

# Envoi du blob de logs complet (avant reboot). Best-effort.
send_logs() {
    local logfile="${1:-$INSTALL_LOG}"
    [ -f "$logfile" ] || return 0
    curl --cacert "$CACERT" $CURL_RETRY_TEL --max-time 20 --silent --output /dev/null \
        -X POST "${HTTPS_BASE}/api/v1/logs" \
        -H "Content-Type: text/plain" \
        -H "X-Machine-MAC: ${MAC}" \
        -H "X-Boot-Id: ${BOOT_ID}" \
        --data-binary @"$logfile" \
        2>/dev/null || true
}

# Confirmation finale (machine a etats serveur). Desormais en HTTPS.
send_confirmation() {
    local status="$1"      # success ou error
    local error_code="$2"  # optionnel: signature_invalid, download_failed, etc.

    local mac="${MAC:-$(get_mac_address)}"
    [ -z "$mac" ] && mac="UNKNOWN"

    local url="${HTTPS_BASE}/confirm/${mac}?status=${status}"
    if [ -n "$error_code" ]; then
        url="${url}&code=${error_code}"
    fi

    log "Envoi confirmation au serveur: status=${status} code=${error_code:-none}"

    local response
    if response=$(curl --cacert "$CACERT" $CURL_RETRY_DL -f -s --connect-timeout 10 --max-time 30 "$url" 2>/dev/null); then
        log "Confirmation envoyee: $response"
        return 0
    else
        log_warn "Echec envoi confirmation (non bloquant)"
        return 1
    fi
}

do_reboot() {
    local delay="${1:-3}"

    log "Reboot dans ${delay} secondes..."
    sleep "$delay"
    sync

    reboot -f 2>/dev/null || \
    echo b > /proc/sysrq-trigger 2>/dev/null || \
    reboot

    sleep 10
    exit 0
}

# === MAIN ===

# Reboot avec envoi du blob de logs juste avant (post-mortem sans ecran).
reboot_with_logs() {
    local delay="${1:-3}"
    send_logs "$INSTALL_LOG"
    do_reboot "$delay"
}

main() {
    local retry_count=0
    local sig_file="/tmp/final_image.sig"
    local hash_file="/tmp/image_hash.txt"

    : > "$INSTALL_LOG" 2>/dev/null || true   # repartir d'un journal vierge

    log_section "AUTO-INSTALLER RPI OS - DEMARRAGE"
    set_progress 0
    init_identity                            # remplit MAC + BOOT_ID
    send_event boot_start start 0 "Demarrage installer"
    log "PID: $$ | PPID: $PPID"
    log "Date: $(date)"
    log "Target: ${TARGET_DEVICE}"
    log "Image URL: ${IMAGE_URL}"
    log "Signature URL: ${SIG_URL}"
    log "Public Key: ${PUBLIC_KEY}"
    log "Kernel: $(uname -r)"

    # Attendre le device
    send_event wait_device start 0 "Attente carte SD" '{"timeout_s":30}'
    if ! wait_for_device "${TARGET_DEVICE}" 30; then
        log_error "Device non disponible - reboot"
        send_event wait_device error 0 "Device ${TARGET_DEVICE} introuvable" '{"error_code":"no_sdcard"}'
        reboot_with_logs 60
    fi
    set_progress 5
    send_event device_ready ok 5 "Carte SD detectee"

    while [ $retry_count -lt $MAX_RETRIES ]; do
        retry_count=$((retry_count + 1))
        ATTEMPT=$retry_count                 # utilise par send_event
        log_section "TENTATIVE ${retry_count}/${MAX_RETRIES}"

        if ! check_network; then
            log_error "Pas de reseau - retry dans ${RETRY_DELAY}s"
            send_event network_check error 5 "Pas de connectivite" '{"error_code":"no_network"}'
            sleep $RETRY_DELAY
            continue
        fi
        set_progress 10

        # Attendre que le canal HTTPS soit reellement etabli avant le 1er appel
        # (evite le "bad record MAC" du reseau pas encore stable au demarrage).
        wait_server_ready 30

        send_event network_check ok 10 "Reseau OK"

        # Telecharger la signature d'abord
        if ! download_signature "${SIG_URL}" "${sig_file}"; then
            log_error "Echec telechargement signature - retry dans ${RETRY_DELAY}s"
            send_event download_signature error 10 "Echec telechargement signature" '{"error_code":"signature_download"}'
            sleep $RETRY_DELAY
            continue
        fi
        set_progress 20
        send_event download_signature ok 20 "Signature telechargee"

        safe_release_device "${TARGET_DEVICE}"

        local start_time
        start_time=$(date +%s)

        set_progress 25
        send_event stream_flash start 25 "Streaming + flash en cours"
        # Streaming + flash avec calcul hash
        if stream_and_flash "${IMAGE_URL}" "${TARGET_DEVICE}"; then
            local end_time duration
            end_time=$(date +%s)
            duration=$((end_time - start_time))

            sync
            sync
            sleep 2

            # Carte non bootable tant que la signature n'est pas validee : une
            # coupure de courant ici fait repartir le Pi en boot reseau.
            set_boot_signature "${TARGET_DEVICE}" off

            set_progress 90
            log "Duree flash: ${duration} secondes"
            send_event flash_done ok 90 "Flash termine (${duration}s)"

            # Verifier la signature RSA
            send_event verify_signature start 92 "Verification signature RSA"
            if verify_signature "${sig_file}" "${hash_file}" "${PUBLIC_KEY}"; then
                set_boot_signature "${TARGET_DEVICE}" on
                log_section "SIGNATURE VALIDE - INSTALLATION REUSSIE"
                set_progress 92

                # Telemetrie succes + confirmation machine a etats (les deux, cf. guide)
                local img_hash
                img_hash=$(cat "$hash_file" 2>/dev/null)
                send_event install_success ok 95 "Signature valide" "{\"valid\":true,\"hash\":\"sha256:$(json_escape "$img_hash")\"}"
                send_confirmation "success"
                set_progress 95

                # Cleanup
                rm -f "${sig_file}" "${hash_file}"

                set_progress 100
                send_event reboot ok 100 "Reboot"
                reboot_with_logs 5
            else
                log_section "SIGNATURE INVALIDE - WIPE ET RETRY"

                send_event verify_signature error 92 "Signature RSA invalide" '{"error_code":"signature_invalid","valid":false}'
                send_confirmation "error" "signature_invalid"

                wipe_device "${TARGET_DEVICE}"
                rm -f "${sig_file}" "${hash_file}"
                sleep $RETRY_DELAY
                continue
            fi
        else
            log_error "Flash echoue - retry dans ${RETRY_DELAY}s"

            send_event stream_flash error 25 "Flash echoue" \
                "{\"error_code\":\"stream_failed\",\"curl_exit\":${STREAM_CURL_EXIT},\"xz_exit\":${STREAM_XZ_EXIT},\"dd_exit\":${STREAM_DD_EXIT}}"
            send_confirmation "error" "download_failed"

            sleep $RETRY_DELAY
            continue
        fi
    done

    log_section "ECHEC APRES ${MAX_RETRIES} TENTATIVES"
    send_event reboot error 0 "Abandon apres ${MAX_RETRIES} tentatives" '{"error_code":"max_retries"}'
    reboot_with_logs 60
}

main "$@"
