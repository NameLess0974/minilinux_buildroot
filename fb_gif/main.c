#include <stdio.h>
#include <stdlib.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/ioctl.h>
#include <linux/fb.h>
#include <stdint.h>
#include <time.h>
#include <signal.h>
#include <linux/kd.h>

#define STB_IMAGE_IMPLEMENTATION
#include "stb_image.h"

void sleep_ms(int ms) {
    struct timespec ts;
    ts.tv_sec = ms / 1000;
    ts.tv_nsec = (ms % 1000) * 1000000;
    nanosleep(&ts, NULL);
}

// Cacher le curseur de la console texte
int tty_fd = -1;

void set_graphics_mode() {
    // Il faut probablement sudo pour ouvrir tty0 en RDWR
    tty_fd = open("/dev/tty0", O_RDWR);
    if (tty_fd >= 0) {
        write(tty_fd, "\033[?25l", 6); // Cacher curseur
        ioctl(tty_fd, KDSETMODE, KD_GRAPHICS); // Désactiver affichage texte Linux
    } else {
        printf("Attention: impossible de passer en mode graphique pur (lancez avec sudo ?)\n");
    }
}

// Fonction pour récupérer la taille du fichier
long get_file_size(FILE *f) {
    fseek(f, 0, SEEK_END);
    long fsize = ftell(f);
    fseek(f, 0, SEEK_SET);
    return fsize;
}

int main(int argc, char *argv[]) {
    int fbfd;
    struct fb_var_screeninfo vinfo;
    struct fb_fix_screeninfo finfo;
    long int screensize;
    char *fbp;
    const char* filename = (argc > 1) ? argv[1] : "loading.gif";

    // Mettre la console Linux en mode graphe pur
    set_graphics_mode();

    // 1. Ouverture du framebuffer
    fbfd = open("/dev/fb0", O_RDWR);
    if (fbfd == -1) {
        perror("Erreur : impossible d'ouvrir /dev/fb0");
        return 1;
    }

    if (ioctl(fbfd, FBIOGET_FSCREENINFO, &finfo) == -1) {
        perror("Erreur : ioctl FBIOGET_FSCREENINFO");
        close(fbfd);
        return 1;
    }
    if (ioctl(fbfd, FBIOGET_VSCREENINFO, &vinfo) == -1) {
        perror("Erreur : ioctl FBIOGET_VSCREENINFO");
        close(fbfd);
        return 1;
    }

    printf("Ecran %dx%d, %d bpp\n", vinfo.xres, vinfo.yres, vinfo.bits_per_pixel);
    screensize = vinfo.yres_virtual * finfo.line_length;

    // 2. Mapping mémoire
    fbp = (char *)mmap(0, screensize, PROT_READ | PROT_WRITE, MAP_SHARED, fbfd, 0);
    if ((intptr_t)fbp == -1) {
        perror("Erreur : mmap");
        close(fbfd);
        return 1;
    }

    // 3. Charger le GIF
    FILE *f = fopen(filename, "rb");
    if (!f) {
        printf("Erreur : impossible d'ouvrir %s\n", filename);
        munmap(fbp, screensize);
        close(fbfd);
        return 1;
    }
    
    long fsize = get_file_size(f);
    unsigned char *gif_data = malloc(fsize);
    if (!gif_data) {
        perror("Erreur allocation");
        fclose(f);
        return 1;
    }
    fread(gif_data, 1, fsize, f);
    fclose(f);

    int *delays;
    int w, h, frames, comp;
    // Forcer 4 canaux (RGBA)
    unsigned char *pixels = stbi_load_gif_from_memory(gif_data, fsize, &delays, &w, &h, &frames, &comp, 4);
    free(gif_data);

    if (!pixels) {
        printf("Erreur décodage GIF: %s\n", stbi_failure_reason());
        munmap(fbp, screensize);
        close(fbfd);
        return 1;
    }

    printf("GIF: %dx%d, %d frames\n", w, h, frames);

    // Centrage sur l'écran
    int start_x = (vinfo.xres - w) / 2;
    int start_y = (vinfo.yres - h) / 2;
    if (start_x < 0) start_x = 0;
    if (start_y < 0) start_y = 0;
    
    int max_w = (w < (int)vinfo.xres) ? w : vinfo.xres;
    int max_h = (h < (int)vinfo.yres) ? h : vinfo.yres;

    // Effacer complètement l'écran (fond noir) pour cacher les restes du terminal
    memset(fbp, 0, screensize);

    // 4. Boucle d'animation
    while (1) {
        for (int frame = 0; frame < frames; frame++) {
            unsigned char *frame_pixels = pixels + (frame * w * h * 4);
            
            for (int y = 0; y < max_h; y++) {
                int screen_y = start_y + y;
                for (int x = 0; x < max_w; x++) {
                    int screen_x = start_x + x;

                    long int location = (screen_x + vinfo.xoffset) * (vinfo.bits_per_pixel / 8) +
                                      (screen_y + vinfo.yoffset) * finfo.line_length;
                    
                    int p_idx = (y * w + x) * 4;
                    unsigned char r = frame_pixels[p_idx];
                    unsigned char g = frame_pixels[p_idx+1];
                    unsigned char b = frame_pixels[p_idx+2];
                    unsigned char a = frame_pixels[p_idx+3];

                    if (a < 128) continue; 

                    if (vinfo.bits_per_pixel == 32) {
                        *(fbp + location) = b;     // B
                        *(fbp + location + 1) = g; // G
                        *(fbp + location + 2) = r; // R
                        *(fbp + location + 3) = a; // A
                    } else if (vinfo.bits_per_pixel == 16) {
                        unsigned short c = ((r >> 3) << 11) | ((g >> 2) << 5) | (b >> 3);
                        *((unsigned short*)(fbp + location)) = c;
                    }
                }
            }
            int delay = (delays && delays[frame] > 0) ? delays[frame] : 100;
            sleep_ms(delay);
        }
    }

    stbi_image_free(pixels);
    if(delays) stbi_image_free(delays);
    munmap(fbp, screensize);
    close(fbfd);
    
    return 0;
}
