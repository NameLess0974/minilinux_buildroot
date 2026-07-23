# Cross-compilateur Buildroot (ARM64 pour Raspberry Pi)
BUILDROOT_HOST = ../buildroot/output/host/bin
CROSS_GCC      = $(BUILDROOT_HOST)/aarch64-linux-gcc
CROSS_GXX      = $(BUILDROOT_HOST)/aarch64-linux-g++

# Racine staging Buildroot (headers + libs : libde265.a, libstdc++.a)
STAGING = ../buildroot/output/staging

# Utilise le cross-compilateur si disponible, sinon gcc natif (x86 local uniquement)
ifeq ($(shell test -f $(CROSS_GCC) && echo yes),yes)
    CC = $(CROSS_GCC)
    CXX = $(CROSS_GXX)
    ARCH_NOTE = aarch64 (cross-compile Buildroot)
else
    CC = gcc
    CXX = g++
    ARCH_NOTE = x86-64 (natif, NE PAS deployer sur Pi !)
endif

# fb_video : splash screen H.265 via libde265, linke en statique (zero dependance runtime).
TARGET  = fb_video
CFLAGS  = -Wall -O2 -I$(STAGING)/usr/include -pthread
LDFLAGS = -static -L$(STAGING)/usr/lib -lde265 -lm -lpthread

all: $(TARGET)
	@echo ">>> $(TARGET) compile pour : $(ARCH_NOTE)"

# On compile le .c avec gcc (code C idiomatique), et on LINKE avec g++ pour
# tirer libstdc++ (requis par libde265). Statique => aucune .so dans le rootfs.
$(TARGET): fb_video.c
	$(CC) $(CFLAGS) -c fb_video.c -o fb_video.o
	$(CXX) -static -o $@ fb_video.o $(LDFLAGS)
	rm -f fb_video.o

# Compat : ancien nom de cible
video: $(TARGET)

# Compile, strip, ET copie le binaire + le flux HEVC dans l'overlay Buildroot
install install-video: $(TARGET)
	$(BUILDROOT_HOST)/aarch64-linux-strip $(TARGET) 2>/dev/null || strip $(TARGET)
	cp -f $(TARGET) ../buildroot/board/raspberrypi/overlay/usr/local/bin/fb_video
	chmod +x ../buildroot/board/raspberrypi/overlay/usr/local/bin/fb_video
	cp -f loading.hevc ../buildroot/board/raspberrypi/overlay/usr/share/splash/loading.hevc
	@echo ">>> fb_video + loading.hevc installes dans l'overlay Buildroot"

clean:
	rm -f $(TARGET) fb_video.o

.PHONY: all video install install-video clean
