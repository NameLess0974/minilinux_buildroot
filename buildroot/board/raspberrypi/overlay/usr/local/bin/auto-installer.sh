#!/bin/bash
# Auto-installer RPI OS pour Buildroot HTTP Boot
# Telecharge et flash RPI OS sur la carte SD
# Version streaming avec verification signature RSA

# Configuration
BASE_URL="http://172.16.1.226:8080/images"
IMAGE_URL="${BASE_URL}/final_image.img.xz"
SIG_URL="${BASE_URL}/final_image.sig"
TARGET_DEVICE="/dev/mmcblk0"
PUBLIC_KEY="/etc/keys/bootkey-public.pem"
MAX_RETRIES=3
RETRY_DELAY=30

# === LOGGING ===
log() {
    local msg="[RPI-INSTALLER] $*"
    echo "<6>${msg}" > /dev/kmsg 2>/dev/null
    echo "$msg"
}

log_error() {
    local msg="[RPI-INSTALLER] ERROR: $*"
    echo "<3>${msg}" > /dev/kmsg 2>/dev/null
    echo "$msg" >&2
}

log_warn() {
    local msg="[RPI-INSTALLER] WARN: $*"
    echo "<4>${msg}" > /dev/kmsg 2>/dev/null
    echo "$msg"
}

log_section() {
    log "=================================================="
    log "$*"
    log "=================================================="
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

    if ping -c 1 -W 3 172.16.1.226 >/dev/null 2>&1; then
        log "Connectivite serveur local: OK"
        return 0
    fi

    if ping -c 1 -W 3 8.8.8.8 >/dev/null 2>&1; then
        log "Connectivite Internet: OK"
        return 0
    fi

    log_error "Pas de connectivite!"
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

    log "Lancement pipeline streaming..."

    # Pipeline avec tee pour calculer le hash du fichier compresse
    # Utiliser named pipe pour garantir que sha256sum lit tout
    local fifo="/tmp/hash_fifo.$$"
    mkfifo "$fifo"

    # Lancer sha256sum en background
    (sha256sum < "$fifo" | awk '{print $1}' > "$hash_file") &
    local sha_pid=$!

    # Pipeline principal
    curl -f -L -S \
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

    if curl -f -L -S --connect-timeout 30 -o "$output" "$url" 2>/dev/null; then
        log "Signature telechargee: $(ls -la $output)"
        return 0
    elif wget -q --timeout=30 -O "$output" "$url" 2>/dev/null; then
        log "Signature telechargee (wget): $(ls -la $output)"
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

send_confirmation() {
    local status="$1"      # success ou error
    local error_code="$2"  # optionnel: signature_invalid, download_failed, etc.
    local server_base="http://172.16.1.226:8080"
    
    local mac
    mac=$(get_mac_address)
    
    if [ -z "$mac" ]; then
        log_warn "Impossible de determiner la MAC address"
        mac="UNKNOWN"
    fi
    
    local url="${server_base}/confirm/${mac}?status=${status}"
    if [ -n "$error_code" ]; then
        url="${url}&code=${error_code}"
    fi
    
    log "Envoi confirmation au serveur: status=${status} code=${error_code:-none}"
    log "URL: $url"
    
    # Essayer curl puis wget
    local response
    if response=$(curl -f -s --connect-timeout 10 --max-time 30 "$url" 2>/dev/null); then
        log "Confirmation envoyee: $response"
        return 0
    elif response=$(wget -q -O - --timeout=30 "$url" 2>/dev/null); then
        log "Confirmation envoyee (wget): $response"
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

main() {
    local retry_count=0
    local sig_file="/tmp/final_image.sig"
    local hash_file="/tmp/image_hash.txt"

    log_section "AUTO-INSTALLER RPI OS - DEMARRAGE"
    log "PID: $$ | PPID: $PPID"
    log "Date: $(date)"
    log "Target: ${TARGET_DEVICE}"
    log "Image URL: ${IMAGE_URL}"
    log "Signature URL: ${SIG_URL}"
    log "Public Key: ${PUBLIC_KEY}"
    log "Kernel: $(uname -r)"

    # Attendre le device
    if ! wait_for_device "${TARGET_DEVICE}" 30; then
        log_error "Device non disponible - reboot"
        do_reboot 60
    fi

    while [ $retry_count -lt $MAX_RETRIES ]; do
        retry_count=$((retry_count + 1))
        log_section "TENTATIVE ${retry_count}/${MAX_RETRIES}"

        if ! check_network; then
            log_error "Pas de reseau - retry dans ${RETRY_DELAY}s"
            sleep $RETRY_DELAY
            continue
        fi

        # Telecharger la signature d'abord
        if ! download_signature "${SIG_URL}" "${sig_file}"; then
            log_error "Echec telechargement signature - retry dans ${RETRY_DELAY}s"
            sleep $RETRY_DELAY
            continue
        fi

        safe_release_device "${TARGET_DEVICE}"

        local start_time
        start_time=$(date +%s)

        # Streaming + flash avec calcul hash
        if stream_and_flash "${IMAGE_URL}" "${TARGET_DEVICE}"; then
            local end_time duration
            end_time=$(date +%s)
            duration=$((end_time - start_time))

            sync
            sync
            sleep 2

            log "Duree flash: ${duration} secondes"

            # Verifier la signature RSA
            if verify_signature "${sig_file}" "${hash_file}" "${PUBLIC_KEY}"; then
                log_section "SIGNATURE VALIDE - INSTALLATION REUSSIE"

                # Confirmer au serveur que tout est OK
                send_confirmation "success"

                # Cleanup
                rm -f "${sig_file}" "${hash_file}"

                do_reboot 5
            else
                log_section "SIGNATURE INVALIDE - WIPE ET RETRY"
                
                # Signaler l'erreur au serveur
                send_confirmation "error" "signature_invalid"
                
                wipe_device "${TARGET_DEVICE}"
                rm -f "${sig_file}" "${hash_file}"
                sleep $RETRY_DELAY
                continue
            fi
        else
            log_error "Flash echoue - retry dans ${RETRY_DELAY}s"
            
            # Signaler l'erreur au serveur
            send_confirmation "error" "download_failed"
            
            sleep $RETRY_DELAY
            continue
        fi
    done

    log_section "ECHEC APRES ${MAX_RETRIES} TENTATIVES"
    do_reboot 60
}

main "$@"
