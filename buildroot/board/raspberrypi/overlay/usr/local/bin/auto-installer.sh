#!/bin/bash
# Auto-installer RPI OS pour Buildroot HTTP Boot
# Telecharge et flash RPI OS sur la carte SD
# Version streaming avec fallback curl/wget

# Configuration
DOWNLOAD_URL="http://172.16.1.226:8080/rpi-lite.img.xz"
TARGET_DEVICE="/dev/mmcblk0"
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

# === STREAMING METHODS ===

stream_with_curl() {
    local url="$1"
    local device="$2"
    
    log_section "METHODE: CURL STREAMING"
    
    if ! command -v curl >/dev/null 2>&1; then
        log_error "curl non disponible sur ce systeme"
        return 1
    fi
    
    log "curl trouve: $(which curl)"
    log "URL: $url"
    log "Device: $device"
    log "Pipeline: curl -> xz -d -> dd of=$device"
    log "Lancement pipeline (pas de test prealable pour eviter double requete)..."
    
    # Creer fichiers pour capturer stderr de chaque commande
    local curl_err="/tmp/curl_err.log"
    local xz_err="/tmp/xz_err.log"
    local dd_err="/tmp/dd_err.log"
    rm -f "$curl_err" "$xz_err" "$dd_err"
    
    # Lancer le pipeline - SANS status=progress (pas supporte par busybox dd)
    curl -f -L -S \
        --connect-timeout 30 \
        --max-time 1800 \
        "$url" 2>"$curl_err" | \
    xz -d 2>"$xz_err" | \
    dd of="$device" bs=4M conv=fsync oflag=direct 2>"$dd_err"
    
    local pipe_status=("${PIPESTATUS[@]}")
    local curl_exit=${pipe_status[0]}
    local xz_exit=${pipe_status[1]}
    local dd_exit=${pipe_status[2]}
    
    log "Pipeline termine"
    log "Exit codes - curl: $curl_exit | xz: $xz_exit | dd: $dd_exit"
    
    # Afficher les erreurs/infos
    if [ -s "$curl_err" ]; then
        log "curl stderr: $(cat $curl_err | head -5)"
    fi
    if [ -s "$xz_err" ]; then
        log "xz stderr: $(cat $xz_err)"
    fi
    if [ -s "$dd_err" ]; then
        log "dd info: $(cat $dd_err)"
    fi
    
    # Cleanup
    rm -f "$curl_err" "$xz_err" "$dd_err"
    
    # Verifier succes
    if [ $curl_exit -eq 0 ] && [ $xz_exit -eq 0 ] && [ $dd_exit -eq 0 ]; then
        log "Streaming curl: SUCCES"
        return 0
    else
        log_error "Streaming curl: ECHEC"
        [ $curl_exit -ne 0 ] && log_error "curl a echoue (code $curl_exit)"
        [ $xz_exit -ne 0 ] && log_error "xz a echoue (code $xz_exit)"
        [ $dd_exit -ne 0 ] && log_error "dd a echoue (code $dd_exit)"
        return 1
    fi
}

stream_with_wget() {
    local url="$1"
    local device="$2"
    
    log_section "METHODE: WGET STREAMING"
    
    if ! command -v wget >/dev/null 2>&1; then
        log_error "wget non disponible sur ce systeme"
        return 1
    fi
    
    log "wget trouve: $(which wget)"
    log "URL: $url"
    log "Device: $device"
    log "Pipeline: wget -> xz -d -> dd of=$device"
    log "Lancement pipeline..."
    
    # Creer fichiers pour capturer stderr
    local wget_err="/tmp/wget_err.log"
    local xz_err="/tmp/xz_err.log"
    local dd_err="/tmp/dd_err.log"
    rm -f "$wget_err" "$xz_err" "$dd_err"
    
    # Lancer le pipeline - SANS status=progress
    wget -q -O - \
        --timeout=60 \
        "$url" 2>"$wget_err" | \
    xz -d 2>"$xz_err" | \
    dd of="$device" bs=4M conv=fsync oflag=direct 2>"$dd_err"
    
    local pipe_status=("${PIPESTATUS[@]}")
    local wget_exit=${pipe_status[0]}
    local xz_exit=${pipe_status[1]}
    local dd_exit=${pipe_status[2]}
    
    log "Pipeline termine"
    log "Exit codes - wget: $wget_exit | xz: $xz_exit | dd: $dd_exit"
    
    # Afficher les erreurs/infos
    if [ -s "$wget_err" ]; then
        log "wget stderr: $(cat $wget_err | head -5)"
    fi
    if [ -s "$xz_err" ]; then
        log "xz stderr: $(cat $xz_err)"
    fi
    if [ -s "$dd_err" ]; then
        log "dd info: $(cat $dd_err)"
    fi
    
    # Cleanup
    rm -f "$wget_err" "$xz_err" "$dd_err"
    
    # Verifier succes
    if [ $wget_exit -eq 0 ] && [ $xz_exit -eq 0 ] && [ $dd_exit -eq 0 ]; then
        log "Streaming wget: SUCCES"
        return 0
    else
        log_error "Streaming wget: ECHEC"
        [ $wget_exit -ne 0 ] && log_error "wget a echoue (code $wget_exit)"
        [ $xz_exit -ne 0 ] && log_error "xz a echoue (code $xz_exit)"
        [ $dd_exit -ne 0 ] && log_error "dd a echoue (code $dd_exit)"
        return 1
    fi
}

download_and_flash() {
    local url="$1"
    local device="$2"
    
    log_section "TELECHARGEMENT ET FLASH"
    log "URL: ${url}"
    log "Destination: ${device}"
    
    # Afficher info systeme
    log "--- Info systeme ---"
    log "RAM totale: $(free -m | awk '/Mem:/ {print $2}') MB"
    log "RAM libre: $(free -m | awk '/Mem:/ {print $4}') MB"
    
    # Detecter outils disponibles
    log "--- Outils disponibles ---"
    command -v curl >/dev/null 2>&1 && log "curl: OUI" || log "curl: NON"
    command -v wget >/dev/null 2>&1 && log "wget: OUI" || log "wget: NON"
    command -v xz >/dev/null 2>&1 && log "xz: OUI" || log "xz: NON"
    command -v dd >/dev/null 2>&1 && log "dd: OUI" || log "dd: NON"
    
    local start_time
    start_time=$(date +%s)
    
    # Essayer curl d'abord
    log "=================================================="
    log "Essai methode 1: curl streaming"
    log "=================================================="
    
    if stream_with_curl "$url" "$device"; then
        local end_time duration
        end_time=$(date +%s)
        duration=$((end_time - start_time))
        
        sync
        sync
        sleep 2
        
        log_section "FLASH TERMINE AVEC SUCCES"
        log "Methode: curl streaming"
        log "Duree: ${duration} secondes"
        
        verify_flash "$device"
        return 0
    fi
    
    log_error "curl streaming a echoue, essai wget..."
    sleep 2
    
    # Essayer wget ensuite
    log "=================================================="
    log "Essai methode 2: wget streaming"
    log "=================================================="
    
    if stream_with_wget "$url" "$device"; then
        local end_time duration
        end_time=$(date +%s)
        duration=$((end_time - start_time))
        
        sync
        sync
        sleep 2
        
        log_section "FLASH TERMINE AVEC SUCCES"
        log "Methode: wget streaming"
        log "Duree: ${duration} secondes"
        
        verify_flash "$device"
        return 0
    fi
    
    log_error "Toutes les methodes ont echoue!"
    return 1
}

verify_flash() {
    local device="$1"
    
    log "--- Verification post-flash ---"
    
    blockdev --rereadpt "${device}" 2>/dev/null
    sleep 2
    
    log "Partitions detectees:"
    ls -la ${device}* 2>/dev/null | while read line; do
        log "  $line"
    done
    
    if [ -b "${device}p1" ]; then
        local p1_size
        p1_size=$(blockdev --getsize64 "${device}p1" 2>/dev/null)
        log "Partition 1: $((p1_size / 1024 / 1024)) MB"
        
        local p1_label
        p1_label=$(blkid -o value -s LABEL "${device}p1" 2>/dev/null)
        log "Partition 1 label: ${p1_label:-none}"
    else
        log_warn "Partition 1 non trouvee"
    fi
    
    if [ -b "${device}p2" ]; then
        local p2_size
        p2_size=$(blockdev --getsize64 "${device}p2" 2>/dev/null)
        log "Partition 2: $((p2_size / 1024 / 1024)) MB"
        
        local p2_label
        p2_label=$(blkid -o value -s LABEL "${device}p2" 2>/dev/null)
        log "Partition 2 label: ${p2_label:-none}"
    else
        log_warn "Partition 2 non trouvee"
    fi
    
    if [ -b "${device}p1" ] && [ -b "${device}p2" ]; then
        log "Verification: OK - 2 partitions presentes"
        return 0
    else
        log_warn "Verification: partitions manquantes"
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
    
    log_section "AUTO-INSTALLER RPI OS - DEMARRAGE"
    log "PID: $$ | PPID: $PPID"
    log "Date: $(date)"
    log "Target: ${TARGET_DEVICE}"
    log "URL: ${DOWNLOAD_URL}"
    log "Kernel: $(uname -r)"
    
    while [ $retry_count -lt $MAX_RETRIES ]; do
        retry_count=$((retry_count + 1))
        log_section "TENTATIVE ${retry_count}/${MAX_RETRIES}"
        
        if ! wait_for_device "${TARGET_DEVICE}" 30; then
            log_error "Device non disponible"
            sleep $RETRY_DELAY
            continue
        fi
        
        if ! check_network; then
            log_error "Pas de reseau - retry dans ${RETRY_DELAY}s"
            sleep $RETRY_DELAY
            continue
        fi
        
        safe_release_device "${TARGET_DEVICE}"
        
        if download_and_flash "${DOWNLOAD_URL}" "${TARGET_DEVICE}"; then
            log_section "INSTALLATION REUSSIE"
            do_reboot 5
        else
            log_error "Flash echoue - retry dans ${RETRY_DELAY}s"
            sleep $RETRY_DELAY
            continue
        fi
    done
    
    log_section "ECHEC APRES ${MAX_RETRIES} TENTATIVES"
    do_reboot 60
}

main "$@"
