# 1. Copier l'EEPROM préconfiguré sur le Pi
scp pieeprom-final.bin pi@ip:~/

# 1.1 Cree un nouveau pieeprom.bin
sudo rpi-eeprom-config --config boot.conf --pubkey bootkey-public.pem --out pieeprom-modifier.bin pieeprom-base.bin
 
# 2. Sur le nouveau Pi, flasher l'EEPROM
sudo rpi-eeprom-update -d -f pieeprom-modifier.bin

# 3. Redémarrer
sudo reboot

# 4. Signer l'image
rpi-eeprom-digest -i boot.img -o boot.sig -k bootkey-private.pem
