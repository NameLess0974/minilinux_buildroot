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

#define STB_TRUETYPE_IMPLEMENTATION
#include "stb_truetype.h"

void sleep_ms(int ms) {
    struct timespec ts;
    ts.tv_sec = ms / 1000;
    ts.tv_nsec = (ms % 1000) * 1000000;
    nanosleep(&ts, NULL);
}

stbtt_fontinfo font_info;
float font_scale;
int font_ascent, font_descent, font_lineGap;
unsigned char *ttf_buffer = NULL;

void init_font(const char *font_path, float pixel_height) {
    FILE *f = fopen(font_path, "rb");
    if (!f) {
        printf("Attention: impossible d'ouvrir la police %s\n", font_path);
        return;
    }
    fseek(f, 0, SEEK_END);
    long size = ftell(f);
    fseek(f, 0, SEEK_SET);
    ttf_buffer = malloc(size);
    if (fread(ttf_buffer, 1, size, f) != (size_t)size) {
        printf("Attention: lecture police incomplete\n");
    }
    fclose(f);

    if (!stbtt_InitFont(&font_info, ttf_buffer, stbtt_GetFontOffsetForIndex(ttf_buffer,0))) {
        printf("Attention: echec init police\n");
    }
    font_scale = stbtt_ScaleForPixelHeight(&font_info, pixel_height);
    stbtt_GetFontVMetrics(&font_info, &font_ascent, &font_descent, &font_lineGap);
}

void draw_text_ttf(int x, int y, const char *text, uint8_t r, uint8_t g, uint8_t b, char *fbp, struct fb_var_screeninfo *vinfo, struct fb_fix_screeninfo *finfo) {
    if (!ttf_buffer) return;
    int cursor_x = x;
    int baseline = y + (int)(font_ascent * font_scale);
    
    while (*text) {
        int advance, lsb, x0, y0, x1, y1;
        int c = *text;
        
        stbtt_GetCodepointHMetrics(&font_info, c, &advance, &lsb);
        stbtt_GetCodepointBitmapBox(&font_info, c, font_scale, font_scale, &x0, &y0, &x1, &y1);
        
        int width = x1 - x0;
        int height = y1 - y0;
        
        if (width > 0 && height > 0) {
            unsigned char *bitmap = malloc(width * height);
            stbtt_MakeCodepointBitmap(&font_info, bitmap, width, height, width, font_scale, font_scale, c);
            
            for (int j = 0; j < height; ++j) {
                for (int i = 0; i < width; ++i) {
                    unsigned char alpha = bitmap[j * width + i];
                    if (alpha > 0) {
                        int sx = cursor_x + x0 + i;
                        int sy = baseline + y0 + j;
                        
                        if (sx >= 0 && sx < (int)vinfo->xres && sy >= 0 && sy < (int)vinfo->yres) {
                            long int loc = (sx + vinfo->xoffset) * (vinfo->bits_per_pixel / 8) +
                                           (sy + vinfo->yoffset) * finfo->line_length;
                            
                            if (vinfo->bits_per_pixel == 32) {
                                unsigned char bg_b = *(fbp + loc);
                                unsigned char bg_g = *(fbp + loc + 1);
                                unsigned char bg_r = *(fbp + loc + 2);
                                
                                *(fbp + loc) = (b * alpha + bg_b * (255 - alpha)) / 255;
                                *(fbp + loc + 1) = (g * alpha + bg_g * (255 - alpha)) / 255;
                                *(fbp + loc + 2) = (r * alpha + bg_r * (255 - alpha)) / 255;
                                *(fbp + loc + 3) = 255;
                            } else if (vinfo->bits_per_pixel == 16) {
                                unsigned short bg_c = *((unsigned short*)(fbp + loc));
                                unsigned char bg_r = (bg_c >> 11) << 3;
                                unsigned char bg_g = ((bg_c >> 5) & 0x3F) << 2;
                                unsigned char bg_b = (bg_c & 0x1F) << 3;
                                
                                unsigned char final_r = (r * alpha + bg_r * (255 - alpha)) / 255;
                                unsigned char final_g = (g * alpha + bg_g * (255 - alpha)) / 255;
                                unsigned char final_b = (b * alpha + bg_b * (255 - alpha)) / 255;
                                
                                unsigned short final_c = ((final_r >> 3) << 11) | ((final_g >> 2) << 5) | (final_b >> 3);
                                *((unsigned short*)(fbp + loc)) = final_c;
                            }
                        }
                    }
                }
            }
            free(bitmap);
        }
        
        cursor_x += (int)(advance * font_scale);
        if (text[1]) {
            cursor_x += (int)(font_scale * stbtt_GetCodepointKernAdvance(&font_info, text[0], text[1]));
        }
        ++text;
    }
}

// Cacher le curseur de la console texte
int tty_fd = -1;

void set_graphics_mode() {
    // Il faut probablement sudo pour ouvrir tty0 en RDWR
    tty_fd = open("/dev/tty0", O_RDWR);
    if (tty_fd >= 0) {
        { ssize_t _r = write(tty_fd, "\033[?25l", 6); (void)_r; } // Cacher curseur
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

    // Allocation du back buffer pour double-buffering evitant les clignotements
    char *back_buf = (char *)malloc(screensize);
    if (!back_buf) {
        perror("Erreur : alloc back_buf");
        munmap(fbp, screensize);
        close(fbfd);
        return 1;
    }

    // Charger la police TTF - chemin absolu pour fonctionner depuis n'importe quel CWD (service systemd)
    if (access("/usr/share/splash/font.ttf", R_OK) == 0) {
        init_font("/usr/share/splash/font.ttf", 72.0f);
    } else {
        init_font("font.ttf", 72.0f); // fallback pour tests locaux
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
    if (fread(gif_data, 1, fsize, f) != (size_t)fsize) {
        printf("Attention: lecture GIF incomplete\n");
    }
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
    memset(back_buf, 0, screensize);

    // === PRE-CACHE DES TEXTURES DE POURCENTAGE (0% a 100%) ===
    // Rendu TTF une seule fois au demarrage dans 101 mini-buffers
    // Chaque frame : blit instantane (memcpy) sans rasterization TTF
    #define TEXT_CACHE_W 400
    #define TEXT_CACHE_H 100
    int bpp_bytes = vinfo.bits_per_pixel / 8;
    int cache_stride = TEXT_CACHE_W * bpp_bytes;
    int cache_size   = TEXT_CACHE_W * TEXT_CACHE_H * bpp_bytes;

    // Simuler un mini-framebuffer pour draw_text_ttf
    struct fb_var_screeninfo cache_vinfo = vinfo;
    cache_vinfo.xres = TEXT_CACHE_W;
    cache_vinfo.yres = TEXT_CACHE_H;
    cache_vinfo.xoffset = 0;
    cache_vinfo.yoffset = 0;
    struct fb_fix_screeninfo cache_finfo = finfo;
    cache_finfo.line_length = cache_stride;

    // Allouer 101 buffers (un par valeur de 0% a 100%)
    unsigned char *text_cache[101];
    for (int p = 0; p <= 100; p++) {
        text_cache[p] = (unsigned char *)calloc(1, cache_size);
        if (!text_cache[p]) continue;

        // Fond noir
        memset(text_cache[p], 0, cache_size);
        // Mettre l'alpha a 255 en 32bpp
        if (vinfo.bits_per_pixel == 32) {
            for (int i = 3; i < cache_size; i += 4)
                text_cache[p][i] = 255;
        }

        // Mesurer la largeur du texte pour centrer dans le cache
        char buf[16];
        snprintf(buf, sizeof(buf), "%d%%", p);
        int tw = 0;
        if (ttf_buffer) {
            for (int i = 0; buf[i]; i++) {
                int adv, lsb;
                stbtt_GetCodepointHMetrics(&font_info, buf[i], &adv, &lsb);
                tw += (int)(adv * font_scale);
            }
        } else { tw = 100; }

        int cache_tx = (TEXT_CACHE_W - tw) / 2;
        draw_text_ttf(cache_tx, 10, buf, 255, 255, 255,
                      (char *)text_cache[p], &cache_vinfo, &cache_finfo);
    }

    // Position fixe du texte : au centre + 12% plus bas
    int text_ty   = ((int)vinfo.yres - TEXT_CACHE_H) / 2 + (int)(vinfo.yres * 0.12f);
    int text_blit_x = ((int)vinfo.xres - TEXT_CACHE_W) / 2;
    if (text_ty < 0)     text_ty = 0;
    if (text_blit_x < 0) text_blit_x = 0;
    // ==========================================================

    // 4. Boucle d'animation
    int percent = 0;
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
                        *(back_buf + location) = b;     // B
                        *(back_buf + location + 1) = g; // G
                        *(back_buf + location + 2) = r; // R
                        *(back_buf + location + 3) = a; // A
                    } else if (vinfo.bits_per_pixel == 16) {
                        unsigned short c = ((r >> 3) << 11) | ((g >> 2) << 5) | (b >> 3);
                        *((unsigned short*)(back_buf + location)) = c;
                    }
                }
            }

            // === BLIT DU CACHE TEXTE (ultra-rapide, remplace le rendu TTF par frame) ===
            // On blitte le buffer pre-rendu du % courant directement dans le back buffer
            if (percent >= 0 && percent <= 100 && text_cache[percent]) {
                for (int cy = 0; cy < TEXT_CACHE_H; cy++) {
                    int screen_y = text_ty + cy;
                    if (screen_y < 0 || screen_y >= (int)vinfo.yres) continue;
                    long int fb_off = (long int)(text_blit_x + vinfo.xoffset) * bpp_bytes
                                    + (long int)(screen_y  + vinfo.yoffset)  * finfo.line_length;
                    int cache_off = cy * cache_stride;
                    memcpy(back_buf + fb_off, text_cache[percent] + cache_off,
                           TEXT_CACHE_W * bpp_bytes);
                }
            }
            // =====================================================================

            // === DOUBLE BUFFERING : Envoi du back buffer vers l'écran ===
            // L'écran ne verra jamais le GIF sans le texte
            memcpy(fbp, back_buf, screensize);

            // Lire le pourcentage réel depuis /tmp/progress (écrit par auto-installer.sh)
            {
                FILE *pf = fopen("/tmp/progress", "r");
                if (pf) {
                    int new_pct = -1;
                    if (fscanf(pf, "%d", &new_pct) == 1 && new_pct >= 0 && new_pct <= 100) {
                        percent = new_pct;
                    }
                    fclose(pf);
                }
            }

            int delay = (delays && delays[frame] > 0) ? delays[frame] : 100;
            sleep_ms(delay);
        }
    }

    for (int p = 0; p <= 100; p++) {
        if (text_cache[p]) free(text_cache[p]);
    }
    stbi_image_free(pixels);
    if(delays) stbi_image_free(delays);
    if(ttf_buffer) free(ttf_buffer);
    free(back_buf);
    munmap(fbp, screensize);
    close(fbfd);
    
    return 0;
}
