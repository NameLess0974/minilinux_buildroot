# Cross-compilateur Buildroot (ARM64 pour Raspberry Pi)
BUILDROOT_HOST = ../buildroot/output/host/bin
CROSS_GCC      = $(BUILDROOT_HOST)/aarch64-linux-gcc

# Utilise le cross-compilateur si disponible, sinon gcc natif (x86 local uniquement)
ifeq ($(shell test -f $(CROSS_GCC) && echo yes),yes)
    CC = $(CROSS_GCC)
    ARCH_NOTE = aarch64 (cross-compile Buildroot)
else
    CC = gcc
    ARCH_NOTE = x86-64 (natif, NE PAS deployer sur Pi !)
endif

CFLAGS  = -Wall -O2
LDFLAGS = -static -lm
TARGET  = fb_gif

all: $(TARGET)
	@echo ">>> Binaire compile pour : $(ARCH_NOTE)"

$(TARGET): main.c
	$(CC) $(CFLAGS) -o $@ $^ $(LDFLAGS)

# Compile ET copie directement dans l'overlay Buildroot
install: $(TARGET)
	cp -f $(TARGET) ../buildroot/board/raspberrypi/overlay/usr/local/bin/fb_gif
	chmod +x ../buildroot/board/raspberrypi/overlay/usr/local/bin/fb_gif
	@echo ">>> fb_gif installe dans l'overlay Buildroot"

clean:
	rm -f $(TARGET)
