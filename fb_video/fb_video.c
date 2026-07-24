// fb_video.c — Splash screen H.265 (HEVC) sur framebuffer, sans dependance graphique.
//
// Meme logique que fb_gif (main.c) : mode graphique pur, police TTF, cache 0-100%,
// double-buffering, lecture de /tmp/progress. Seule difference : le decodage GIF est
// remplace par un decodage HEVC brut (Annex-B) via libde265.
//
// Le flux .hevc est charge entierement en RAM (petit fichier), puis re-decode a chaque
// boucle d'animation. On ne pre-decode PAS en RGB (140 frames 1080p ~ 1 Go), on decode
// image par image et on blit a la volee.

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <fcntl.h>
#include <unistd.h>
#include <sys/mman.h>
#include <sys/ioctl.h>
#include <linux/fb.h>
#include <stdint.h>
#include <math.h>
#include <time.h>
#include <signal.h>
#include <linux/kd.h>
#include <linux/vt.h>
#include <linux/input.h>
#include <dirent.h>
#include <pthread.h>

#include <libde265/de265.h>

#define STB_TRUETYPE_IMPLEMENTATION
#include "stb_truetype.h"

// ============================ MODE DEBUG ============================
// DEBUG=1 : PAS de splash video. On bascule directement sur la console des
//           logs (auto-installer) et on AFFICHE chaque touche percue (nom+code)
//           pour diagnostiquer le clavier. DEBUG=0 : splash video normal, avec
//           la combo secrete Menu x5 + Enter pour basculer en debug a la volee.
#define DEBUG 0

void sleep_ms(int ms) {
    struct timespec ts;
    ts.tv_sec = ms / 1000;
    ts.tv_nsec = (ms % 1000) * 1000000;
    nanosleep(&ts, NULL);
}

// ============================ POLICE TTF (identique a main.c) ============================
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

// Rendu du texte dans une CARTE D'ALPHA 8 bits (couverture 0..255), 1 octet/pixel.
// Utilise pour compositer le % par-dessus la video SANS fond noir (transparence).
// `scale` : echelle stbtt (permet un rendu plus petit que la police globale).
void draw_text_alpha_scaled(int x, int y, const char *text, float scale,
                            unsigned char *amap, int amap_w, int amap_h) {
    if (!ttf_buffer) return;
    int cursor_x = x;
    int baseline = y + (int)(font_ascent * scale);

    while (*text) {
        int advance, lsb, x0, y0, x1, y1;
        int c = *text;
        stbtt_GetCodepointHMetrics(&font_info, c, &advance, &lsb);
        stbtt_GetCodepointBitmapBox(&font_info, c, scale, scale, &x0, &y0, &x1, &y1);

        int width = x1 - x0;
        int height = y1 - y0;
        if (width > 0 && height > 0) {
            unsigned char *bitmap = malloc(width * height);
            stbtt_MakeCodepointBitmap(&font_info, bitmap, width, height, width, scale, scale, c);
            for (int j = 0; j < height; ++j) {
                for (int i = 0; i < width; ++i) {
                    unsigned char a = bitmap[j * width + i];
                    if (!a) continue;
                    int sx = cursor_x + x0 + i;
                    int sy = baseline + y0 + j;
                    if (sx >= 0 && sx < amap_w && sy >= 0 && sy < amap_h) {
                        unsigned char *dst = &amap[sy * amap_w + sx];
                        if (a > *dst) *dst = a; // garder la couverture max
                    }
                }
            }
            free(bitmap);
        }
        cursor_x += (int)(advance * scale);
        if (text[1]) cursor_x += (int)(scale * stbtt_GetCodepointKernAdvance(&font_info, text[0], text[1]));
        ++text;
    }
}

// Variante a l'echelle globale (police 72px), conservee pour compat.
void draw_text_alpha(int x, int y, const char *text, unsigned char *amap, int amap_w, int amap_h) {
    draw_text_alpha_scaled(x, y, text, font_scale, amap, amap_w, amap_h);
}

// ============================ MODE DEBUG : combo clavier ============================
// Sequence secrete : Menu x5 puis Enter x1 -> bascule vers la console des logs.
// Un thread lit tous les /dev/input/event* et leve ce flag quand la combo est faite.
#define DEBUG_TTY 3          // console=tty3 : c'est la que l'auto-installer ecrit
#define MENU_NEEDED 5

static volatile int g_debug_requested = 0;

// En DEBUG=1 : ecrit le nom/code de la touche percue sur la console des logs,
// pour verifier que le clavier est bien lu. Ecrit sur tty3 (ou /dev/console).
#if DEBUG
static void debug_report_key(int code, int value) {
    const char *name = "?";
    switch (code) {
        case KEY_MENU:    name = "MENU";    break;
        case KEY_COMPOSE: name = "COMPOSE"; break;
        case KEY_ENTER:   name = "ENTER";   break;
        case KEY_KPENTER: name = "KP_ENTER";break;
        case KEY_ESC:     name = "ESC";     break;
        case KEY_SPACE:   name = "SPACE";   break;
        default: break;
    }
    char buf[96];
    int n = snprintf(buf, sizeof(buf), "[key] code=%d (%s) %s\n",
                     code, name, value == 1 ? "PRESS" : (value == 0 ? "release" : "repeat"));
    int t = open("/dev/tty3", O_WRONLY);
    if (t < 0) t = open("/dev/console", O_WRONLY);
    if (t >= 0) { ssize_t _r = write(t, buf, n); (void)_r; close(t); }
}
#endif

// Lit un device evdev et met a jour l'etat de la sequence.
// Retour : 1 si la combo complete a ete detectee.
static int watch_evdev_fd(int fd, int *menu_count) {
    struct input_event ev;
    ssize_t r;
    while ((r = read(fd, &ev, sizeof(ev))) == (ssize_t)sizeof(ev)) {
        if (ev.type != EV_KEY) continue;
#if DEBUG
        // En debug on rapporte chaque appui (et repeat) pour diagnostiquer le clavier
        if (ev.value == 1 || ev.value == 2) debug_report_key(ev.code, ev.value);
#endif
        if (ev.value != 1) continue; // pour la combo : seulement les appuis (press)
        if (ev.code == KEY_MENU || ev.code == KEY_COMPOSE) {
            if (*menu_count < MENU_NEEDED) (*menu_count)++;
        } else if (ev.code == KEY_ENTER || ev.code == KEY_KPENTER) {
            if (*menu_count >= MENU_NEEDED) return 1; // combo OK
            *menu_count = 0; // Enter trop tot : on repart de zero
        } else {
            *menu_count = 0; // toute autre touche annule la sequence
        }
    }
    return 0;
}

// Thread : ouvre tous les claviers /dev/input/event* et surveille la combo.
static void *input_thread(void *arg) {
    (void)arg;
    int fds[32]; int nfd = 0;
    int menu_count = 0;

    DIR *d = opendir("/dev/input");
    if (d) {
        struct dirent *e;
        while ((e = readdir(d)) != NULL && nfd < 32) {
            if (strncmp(e->d_name, "event", 5) != 0) continue;
            char path[300];
            snprintf(path, sizeof(path), "/dev/input/%s", e->d_name);
            int fd = open(path, O_RDONLY | O_NONBLOCK);
            if (fd >= 0) fds[nfd++] = fd;
        }
        closedir(d);
    }
    if (nfd == 0) {
#if DEBUG
        // Diagnostic : signaler explicitement qu'aucun clavier n'a ete trouve
        int t = open("/dev/tty3", O_WRONLY);
        if (t < 0) t = open("/dev/console", O_WRONLY);
        if (t >= 0) {
            const char *m = "[key] AUCUN /dev/input/event* trouve (pas de clavier detecte)\n";
            ssize_t _r = write(t, m, strlen(m)); (void)_r; close(t);
        }
#endif
        return NULL; // pas de clavier : mode debug indisponible (pas grave)
    }

    while (!g_debug_requested) {
        for (int i = 0; i < nfd; i++) {
            if (watch_evdev_fd(fds[i], &menu_count)) {
#if !DEBUG
                // En mode normal, la combo bascule vers la console. En DEBUG on
                // est deja sur la console : on continue a rapporter les touches.
                g_debug_requested = 1;
                break;
#endif
            }
        }
        sleep_ms(30);
    }
    for (int i = 0; i < nfd; i++) close(fds[i]);
    return NULL;
}

// Bascule l'ecran vers la console des logs (tty3) et redonne le mode texte.
static int g_tty0_fd = -1; // rempli par set_graphics_mode
static void enter_debug_console(void) {
    // Reafficher le curseur et repasser tty0 en mode TEXTE
    if (g_tty0_fd >= 0) {
        { ssize_t _r = write(g_tty0_fd, "\033[?25h", 6); (void)_r; }
        ioctl(g_tty0_fd, KDSETMODE, KD_TEXT);
    }
    // Activer la console ou ecrit l'auto-installer (console=tty3).
    // On NE fait PAS VT_WAITACTIVE (il peut bloquer indefiniment si le VT
    // n'est pas activable sous KMS) : VT_ACTIVATE seul suffit, et on continue.
    int c = open("/dev/tty0", O_RDWR);
    if (c >= 0) {
        ioctl(c, VT_ACTIVATE, DEBUG_TTY);
        close(c);
    }
    // Message de confirmation ecrit directement sur la console des logs,
    // pour prouver que la combo a bien ete captee meme si le VT switch echoue.
    int t = open("/dev/tty3", O_WRONLY);
    if (t < 0) t = open("/dev/console", O_WRONLY);
    if (t >= 0) {
        const char *msg = "\n\n=== MODE DEBUG ACTIVE (Menu x5 + Enter) : logs auto-installer ===\n\n";
        { ssize_t _r = write(t, msg, strlen(msg)); (void)_r; }
        close(t);
    }
}


// ============================ MODE GRAPHIQUE (identique a main.c) ============================
int tty_fd = -1;

void set_graphics_mode() {
    tty_fd = open("/dev/tty0", O_RDWR);
    if (tty_fd >= 0) {
        { ssize_t _r = write(tty_fd, "\033[?25l", 6); (void)_r; } // Cacher curseur
        ioctl(tty_fd, KDSETMODE, KD_GRAPHICS); // Desactiver affichage texte Linux
        g_tty0_fd = tty_fd; // reutilise par le mode debug pour repasser en texte
    } else {
        printf("Attention: impossible de passer en mode graphique pur (lancez avec sudo ?)\n");
    }
}

long get_file_size(FILE *f) {
    fseek(f, 0, SEEK_END);
    long fsize = ftell(f);
    fseek(f, 0, SEEK_SET);
    return fsize;
}

// ============================ CONVERSION YUV420 -> RGB ============================
// clamp rapide vers [0,255]
static inline unsigned char clamp8(int v) {
    if (v < 0) return 0;
    if (v > 255) return 255;
    return (unsigned char)v;
}

// Matrice de Bayer 8x8 (dithering ordonne) pour le rendu 16 bpp (RGB565).
// Sans ca, la reduction 8->5/6 bits provoque un banding "corrompu/pixelise"
// tres visible sur les degrades. En 32 bpp elle n'est pas utilisee.
static const int bayer8[8][8] = {
    {  0, 32,  8, 40,  2, 34, 10, 42 },
    { 48, 16, 56, 24, 50, 18, 58, 26 },
    { 12, 44,  4, 36, 14, 46,  6, 38 },
    { 60, 28, 52, 20, 62, 30, 54, 22 },
    {  3, 35, 11, 43,  1, 33,  9, 41 },
    { 51, 19, 59, 27, 49, 17, 57, 25 },
    { 15, 47,  7, 39, 13, 45,  5, 37 },
    { 63, 31, 55, 23, 61, 29, 53, 21 }
};

// ============================ GLOBALS FRAMEBUFFER (pour le callback de blit) ============================
static struct fb_var_screeninfo vinfo;
static struct fb_fix_screeninfo finfo;
static char *back_buf = NULL;

// Position/centrage de l'image, calcules une fois qu'on connait w/h de la video
static int g_start_x = 0, g_start_y = 0;
static int g_max_w = 0, g_max_h = 0;

// Composite un pixel (r,g,b) avec un alpha 0..255 sur le back buffer a (x,y).
// Factorise le melange 16/32 bpp (utilise par la barre de progression et le texte).
static inline void blend_px(int x, int y, unsigned char r, unsigned char g,
                            unsigned char b, unsigned char a) {
    if (a == 0) return;
    if (x < 0 || x >= (int)vinfo.xres || y < 0 || y >= (int)vinfo.yres) return;
    int bpp_bytes = vinfo.bits_per_pixel / 8;
    long int loc = (long int)(x + vinfo.xoffset) * bpp_bytes
                 + (long int)(y + vinfo.yoffset) * finfo.line_length;
    if (vinfo.bits_per_pixel == 32) {
        unsigned char bg_b = *(back_buf + loc);
        unsigned char bg_g = *(back_buf + loc + 1);
        unsigned char bg_r = *(back_buf + loc + 2);
        *(back_buf + loc)     = (b * a + bg_b * (255 - a)) / 255;
        *(back_buf + loc + 1) = (g * a + bg_g * (255 - a)) / 255;
        *(back_buf + loc + 2) = (r * a + bg_r * (255 - a)) / 255;
        *(back_buf + loc + 3) = 255;
    } else if (vinfo.bits_per_pixel == 16) {
        unsigned short bg_c = *((unsigned short*)(back_buf + loc));
        unsigned char bg_r = (bg_c >> 11) << 3;
        unsigned char bg_g = ((bg_c >> 5) & 0x3F) << 2;
        unsigned char bg_b = (bg_c & 0x1F) << 3;
        unsigned char fr = (r * a + bg_r * (255 - a)) / 255;
        unsigned char fg = (g * a + bg_g * (255 - a)) / 255;
        unsigned char fb = (b * a + bg_b * (255 - a)) / 255;
        *((unsigned short*)(back_buf + loc)) =
            ((fr >> 3) << 11) | ((fg >> 2) << 5) | (fb >> 3);
    }
}

// Dessine une barre de progression a coins arrondis (pilule), remplie a `pct`%.
// (bx,by) = coin haut-gauche ; bw x bh = dimensions. Fond translucide + remplissage
// cyan. Les bords arrondis sont ANTI-ALIASES : on module l'alpha par la couverture
// du pixel (distance au bord du cercle) au lieu d'un test binaire dedans/dehors,
// ce qui elimine l'effet d'escalier. Le remplissage a aussi un bord doux vertical.
static void draw_progress_bar(int bx, int by, int bw, int bh, int pct) {
    if (pct < 0) pct = 0;
    if (pct > 100) pct = 100;
    float radius = bh / 2.0f;            // arrondi = demi-hauteur
    if (radius > bw / 2.0f) radius = bw / 2.0f;
    float fill_w = (bw * pct) / 100.0f;  // largeur remplie (flottant pour bord doux)

    // Couleurs
    const unsigned char track_r = 20,  track_g = 24,  track_b = 30;   // fond sombre
    const float         track_a = 150.0f;                              // translucide
    const unsigned char fill_r = 0,   fill_g = 200, fill_b = 230;      // cyan
    const float         fill_a = 235.0f;

    for (int y = 0; y < bh; y++) {
        for (int x = 0; x < bw; x++) {
            // Couverture de la forme (0..1) : 1 au centre, degrade sur ~1px au bord
            // arrondi. Pour les coins on mesure la distance au centre du cercle.
            float coverage = 1.0f;
            float cx = -1.0f, cy = -1.0f;
            if      (x < radius && y < radius)             { cx = radius;         cy = radius; }
            else if (x >= bw - radius && y < radius)       { cx = bw - radius;    cy = radius; }
            else if (x < radius && y >= bh - radius)       { cx = radius;         cy = bh - radius; }
            else if (x >= bw - radius && y >= bh - radius) { cx = bw - radius;    cy = bh - radius; }
            if (cx >= 0.0f) {
                // distance du centre du pixel (x+0.5,y+0.5) au centre du cercle
                float dx = (x + 0.5f) - cx, dy = (y + 0.5f) - cy;
                float dist = sqrtf(dx * dx + dy * dy);
                // couverture = 1 a l'interieur, 0 au-dela, transition douce de 1px
                coverage = radius - dist + 0.5f;
                if (coverage <= 0.0f) continue;      // totalement hors forme
                if (coverage > 1.0f) coverage = 1.0f;
            }

            // Bord doux vertical du remplissage (transition cyan -> track sur ~1px)
            float fill_frac = fill_w - x;            // >1 : plein, <0 : vide
            if (fill_frac > 1.0f) fill_frac = 1.0f;
            if (fill_frac < 0.0f) fill_frac = 0.0f;

            // Melange track <-> fill selon fill_frac, alpha module par coverage
            unsigned char r = (unsigned char)(fill_r * fill_frac + track_r * (1.0f - fill_frac));
            unsigned char g = (unsigned char)(fill_g * fill_frac + track_g * (1.0f - fill_frac));
            unsigned char b = (unsigned char)(fill_b * fill_frac + track_b * (1.0f - fill_frac));
            float a = (fill_a * fill_frac + track_a * (1.0f - fill_frac)) * coverage;

            blend_px(bx + x, by + y, r, g, b, (unsigned char)(a + 0.5f));
        }
    }
}

// Blit d'une image decodee (planes Y,U,V) dans le back buffer, avec conversion YUV420->RGB.
//
// Qualite : deux ameliorations majeures par rapport au rendu naif (plus-proche-voisin
// + BT.601), qui causait halos, "cercles gris" et couleurs delavees :
//
//   1) CHROMA BILINEAIRE. En 4:2:0 la couleur est en demi-resolution. Prendre
//      simplement urow[x>>1] plaque le meme bloc couleur 2x2 -> paliers visibles
//      sur les degrades (les halos/cercles). On interpole donc U et V dans les
//      DEUX directions (horizontale et verticale) a partir des 4 echantillons
//      chroma voisins, avec le bon decalage de phase (les sites chroma MPEG-2
//      sont co-situes horizontalement au pixel pair) : degrades lisses.
//
//   2) BT.709. Une video 1080p est en Rec.709, pas 601. La matrice 601 decalait
//      legerement les teintes (verts/rouges). On utilise donc la matrice 709
//      "limited range" (Y 16..235, C 16..240), coefficients en virgule fixe .8.
static void blit_yuv420(const struct de265_image *img) {
    int width  = de265_get_image_width(img, 0);
    int height = de265_get_image_height(img, 0);

    int ys, us, vs;
    const uint8_t *yp = de265_get_image_plane(img, 0, &ys);
    const uint8_t *up = de265_get_image_plane(img, 1, &us);
    const uint8_t *vp = de265_get_image_plane(img, 2, &vs);
    if (!yp || !up || !vp) return;

    int cw = (width  + 1) >> 1;   // largeur du plan chroma
    int ch = (height + 1) >> 1;   // hauteur du plan chroma
    int bpp_bytes = vinfo.bits_per_pixel / 8;

    int xmax = (g_max_w < width) ? g_max_w : width;
    int is32 = (vinfo.bits_per_pixel == 32);

    // PERF : la chroma (U,V) est constante sur 2 pixels horizontaux (4:2:0). On la
    // decode donc UNE fois par paire et on pre-calcule les 3 termes chroma de la
    // matrice BT.709 (rv, guv, bu), reutilises pour les 2 pixels luma de la paire.
    // Chroma en NEAREST vertical (ligne y>>1) : le bilineaire coutait ~55 ms/frame
    // (=> 13 fps). En 32 bpp la difference visuelle est nulle, le gain de vitesse
    // enorme. Les termes luma (298*C) restent calcules par pixel.
    for (int y = 0; y < g_max_h && y < height; y++) {
        int screen_y = g_start_y + y;
        const uint8_t *yrow = yp + y * ys;
        const uint8_t *urow = up + (y >> 1) * us;
        const uint8_t *vrow = vp + (y >> 1) * vs;

        long int row_loc = (long int)(g_start_x + vinfo.xoffset) * bpp_bytes +
                           (long int)(screen_y + vinfo.yoffset) * finfo.line_length;
        char *dst = back_buf + row_loc;

        if (is32) {
            // --- chemin 32 bpp (cas nominal) : pas de dithering, ecriture BGRA ---
            for (int x = 0; x < xmax; x += 2) {
                int c = x >> 1;
                int U = urow[c] - 128, V = vrow[c] - 128;
                int rv  = 459 * V + 128;              // termes chroma pre-calcules
                int guv = -55 * U - 136 * V + 128;
                int bu  = 541 * U + 128;
                // pixel pair
                int C0 = (yrow[x] - 16) * 298;
                dst[0] = clamp8((C0 + bu ) >> 8);
                dst[1] = clamp8((C0 + guv) >> 8);
                dst[2] = clamp8((C0 + rv ) >> 8);
                dst[3] = (char)255;
                // pixel impair (meme chroma)
                if (x + 1 < xmax) {
                    int C1 = (yrow[x+1] - 16) * 298;
                    dst[4] = clamp8((C1 + bu ) >> 8);
                    dst[5] = clamp8((C1 + guv) >> 8);
                    dst[6] = clamp8((C1 + rv ) >> 8);
                    dst[7] = (char)255;
                }
                dst += 8;
            }
        } else {
            // --- chemin 16 bpp (fallback si depth=32 non pris) : + dithering ---
            const int *brow = bayer8[screen_y & 7];
            int bx = g_start_x & 7;
            for (int x = 0; x < xmax; x++) {
                int c = x >> 1;
                int U = urow[c] - 128, V = vrow[c] - 128;
                int C = (yrow[x] - 16) * 298;
                unsigned char r = clamp8((C + 459 * V + 128) >> 8);
                unsigned char g = clamp8((C -  55 * U - 136 * V + 128) >> 8);
                unsigned char b = clamp8((C + 541 * U + 128) >> 8);
                int t = brow[bx]; bx = (bx + 1) & 7;
                int rr = (r & ~7) + (((r & 7) * 8 > t) ? 8 : 0);
                int bb = (b & ~7) + (((b & 7) * 8 > t) ? 8 : 0);
                int gg = (g & ~3) + (((g & 3) * 16 > t) ? 4 : 0);
                if (rr > 255) rr = 255;
                if (gg > 255) gg = 255;
                if (bb > 255) bb = 255;
                *((unsigned short*)dst) = ((rr >> 3) << 11) | ((gg >> 2) << 5) | (bb >> 3);
                dst += 2;
            }
        }
    }
}

int main(int argc, char *argv[]) {
    int fbfd;
    long int screensize;
    char *fbp;
    const char* filename = (argc > 1) ? argv[1] : "loading.hevc";

#if DEBUG
    // === MODE DEBUG : pas de splash. On laisse la console des logs visible et
    // on affiche chaque touche percue. On NE passe PAS en mode graphique (le
    // texte console reste affiche). On lance juste le thread clavier (qui, en
    // DEBUG, rapporte les touches via debug_report_key) et on reste vivant
    // (le service a Restart=always : quitter relancerait fb_video en boucle).
    {
        // Basculer l'ecran vers la console des logs (tty3) pour qu'elle soit
        // visible (au boot, le VT affiche est souvent tty1).
        int vt = open("/dev/tty0", O_RDWR);
        if (vt >= 0) { ioctl(vt, VT_ACTIVATE, DEBUG_TTY); close(vt); }

        int t = open("/dev/tty3", O_WRONLY);
        if (t < 0) t = open("/dev/console", O_WRONLY);
        if (t >= 0) {
            const char *msg = "\n=== fb_video : MODE DEBUG (DEBUG=1) : splash desactive, "
                              "logs auto-installer + touches clavier ci-dessous ===\n\n";
            ssize_t _r = write(t, msg, strlen(msg)); (void)_r;
            close(t);
        }
        pthread_t dth;
        if (pthread_create(&dth, NULL, input_thread, NULL) == 0) pthread_detach(dth);
        while (1) pause();
    }
#endif

    set_graphics_mode();

    // --- Ouverture framebuffer ---
    fbfd = open("/dev/fb0", O_RDWR);
    if (fbfd == -1) {
        perror("Erreur : impossible d'ouvrir /dev/fb0");
        return 1;
    }
    if (ioctl(fbfd, FBIOGET_FSCREENINFO, &finfo) == -1) {
        perror("Erreur : ioctl FBIOGET_FSCREENINFO"); close(fbfd); return 1;
    }
    if (ioctl(fbfd, FBIOGET_VSCREENINFO, &vinfo) == -1) {
        perror("Erreur : ioctl FBIOGET_VSCREENINFO"); close(fbfd); return 1;
    }

    printf("Ecran %dx%d, %d bpp\n", vinfo.xres, vinfo.yres, vinfo.bits_per_pixel);
    // Diagnostic : ecrit les infos ecran dans un fichier lisible apres boot
    // (le service redirige stdout vers null). Permet de confirmer le bpp reel.
    {
        FILE *dbg = fopen("/tmp/fb_info", "w");
        if (dbg) {
            fprintf(dbg, "xres=%u yres=%u bpp=%u line_length=%u\n",
                    vinfo.xres, vinfo.yres, vinfo.bits_per_pixel, finfo.line_length);
            fclose(dbg);
        }
    }
    screensize = vinfo.yres_virtual * finfo.line_length;

    fbp = (char *)mmap(0, screensize, PROT_READ | PROT_WRITE, MAP_SHARED, fbfd, 0);
    if ((intptr_t)fbp == -1) { perror("Erreur : mmap"); close(fbfd); return 1; }

    // Double-buffering : on rend dans back_buf puis on copie vers fbp d'un coup.
    // Evite le tearing/clignotement (barre, %) qu'on voyait en ecrivant direct.
    back_buf = (char *)malloc(screensize);
    if (!back_buf) { perror("Erreur : alloc back_buf"); munmap(fbp, screensize); close(fbfd); return 1; }

    // --- Police ---
    if (access("/usr/share/splash/font.ttf", R_OK) == 0) {
        init_font("/usr/share/splash/font.ttf", 72.0f);
    } else {
        init_font("font.ttf", 72.0f);
    }

    // --- Thread de surveillance clavier (mode debug : Menu x5 + Enter) ---
    pthread_t th;
    if (pthread_create(&th, NULL, input_thread, NULL) == 0) {
        pthread_detach(th);
    }

    // --- Charger tout le flux HEVC en RAM ---
    FILE *f = fopen(filename, "rb");
    if (!f) {
        printf("Erreur : impossible d'ouvrir %s\n", filename);
        munmap(fbp, screensize); close(fbfd); return 1;
    }
    long fsize = get_file_size(f);
    unsigned char *hevc_data = malloc(fsize);
    if (!hevc_data) { perror("Erreur allocation"); fclose(f); return 1; }
    if (fread(hevc_data, 1, fsize, f) != (size_t)fsize) {
        printf("Attention: lecture HEVC incomplete\n");
    }
    fclose(f);

    // --- Determiner w/h en decodant la 1re image (pour centrer) ---
    de265_error err;
    de265_decoder_context *ctx = de265_new_decoder();
    de265_start_worker_threads(ctx, 0); // 0 = mono-thread, suffisant ici
    de265_push_data(ctx, hevc_data, fsize, 0, NULL);
    de265_flush_data(ctx);

    int vid_w = 0, vid_h = 0;
    {
        int more = 1;
        while (more) {
            more = 0;
            err = de265_decode(ctx, &more);
            if (err != DE265_OK && more == 0) break;
            const struct de265_image *img = de265_get_next_picture(ctx);
            if (img) {
                vid_w = de265_get_image_width(img, 0);
                vid_h = de265_get_image_height(img, 0);
                de265_release_next_picture(ctx);
                break;
            }
        }
    }
    de265_free_decoder(ctx);

    if (vid_w == 0 || vid_h == 0) {
        printf("Erreur : impossible de decoder la video HEVC %s\n", filename);
        munmap(fbp, screensize); close(fbfd); return 1;
    }
    printf("HEVC: %dx%d\n", vid_w, vid_h);

    // Centrage sur l'ecran (comme main.c)
    g_start_x = ((int)vinfo.xres - vid_w) / 2;
    g_start_y = ((int)vinfo.yres - vid_h) / 2;
    if (g_start_x < 0) g_start_x = 0;
    if (g_start_y < 0) g_start_y = 0;
    g_max_w = (vid_w < (int)vinfo.xres) ? vid_w : (int)vinfo.xres;
    g_max_h = (vid_h < (int)vinfo.yres) ? vid_h : (int)vinfo.yres;

    // Ecran noir
    memset(fbp, 0, screensize);
    memset(back_buf, 0, screensize);

    // === PRE-CACHE DES CARTES D'ALPHA DU POURCENTAGE (0% a 100%) ===
    // On stocke la COUVERTURE du glyphe (1 octet/pixel), pas des pixels finaux.
    // A l'affichage, on composite le % en blanc par-dessus la video (transparent).
    // Le % est rendu CENTRE dans son cache et un peu PLUS PETIT que la police
    // globale (echelle dediee), car il s'affiche sous la barre.
    #define TEXT_CACHE_W 400
    #define TEXT_CACHE_H 80
    float pct_px = 52.0f;                          // taille du % (px), plus petit
    float pct_scale = stbtt_ScaleForPixelHeight(&font_info, pct_px);

    unsigned char *text_alpha[101];
    for (int p = 0; p <= 100; p++) {
        text_alpha[p] = (unsigned char *)calloc(1, TEXT_CACHE_W * TEXT_CACHE_H);
        if (!text_alpha[p]) continue;

        char buf[16];
        snprintf(buf, sizeof(buf), "%d%%", p);
        // Largeur du texte a l'echelle reduite, pour le centrer dans le cache
        int tw = 0;
        if (ttf_buffer) {
            for (int i = 0; buf[i]; i++) {
                int adv, lsb;
                stbtt_GetCodepointHMetrics(&font_info, buf[i], &adv, &lsb);
                tw += (int)(adv * pct_scale);
            }
        } else { tw = 80; }

        int cache_tx = (TEXT_CACHE_W - tw) / 2;    // centre horizontalement
        draw_text_alpha_scaled(cache_tx, 8, buf, pct_scale,
                               text_alpha[p], TEXT_CACHE_W, TEXT_CACHE_H);
    }

    // === LAYOUT : barre centree, % centre juste EN DESSOUS avec un gap ===
    int bar_w = (int)vinfo.xres * 40 / 100;      // 40% de la largeur
    if (bar_w > 900) bar_w = 900;
    if (bar_w < 240) bar_w = 240;
    int bar_h = 18;                               // barre fine
    int gap   = 22;                               // espace barre <-> %

    int group_cy = (int)vinfo.yres / 2;           // centre vertical de l'ecran
    int bar_x = ((int)vinfo.xres - bar_w) / 2;    // barre centree horizontalement
    int bar_y = group_cy - bar_h / 2;

    // Cache texte centre horizontalement sur la barre, place sous la barre + gap
    int text_blit_x = ((int)vinfo.xres - TEXT_CACHE_W) / 2;
    int text_ty     = bar_y + bar_h + gap;
    if (text_ty < 0)     text_ty = 0;
    if (text_blit_x < 0) text_blit_x = 0;
    // ==================================================================

    const int frame_delay_ms = 40; // 25 fps (budget que le Pi tient sans ralenti)

    // === BOUCLE D'ANIMATION : re-decode le flux entier a chaque tour ===
    int percent = 0;
    // Horloge de cadencement (timing adaptatif, voir plus bas). Initialisee ici.
    long long next_frame_ns;
    {
        struct timespec t0;
        clock_gettime(CLOCK_MONOTONIC, &t0);
        next_frame_ns = (long long)t0.tv_sec * 1000000000LL + t0.tv_nsec;
    }
    while (1) {
        de265_decoder_context *dctx = de265_new_decoder();
        de265_start_worker_threads(dctx, 4); // 4 coeurs (Pi4/5) : decodage plus rapide
        de265_push_data(dctx, hevc_data, fsize, 0, NULL);
        de265_flush_data(dctx);

        // Boucle drainante (validee sur le vrai flux) : on decode, puis on affiche
        // toutes les images disponibles, jusqu'a epuisement du flux.
        int more = 1;
        do {
            more = 0;
            de265_decode(dctx, &more);

            const struct de265_image *img;
            while ((img = de265_get_next_picture(dctx)) != NULL) {
                // 1) Dessiner l'image dans le back buffer
                blit_yuv420(img);
                de265_release_next_picture(dctx);

                // 2a) Barre de progression a coins arrondis (cyan), remplie a `percent`%
                draw_progress_bar(bar_x, bar_y, bar_w, bar_h, percent);

                // 2b) Composite le % (blanc) a droite de la barre via la carte d'alpha
                //     (fond transparent, seul le chiffre apparait).
                if (percent >= 0 && percent <= 100 && text_alpha[percent]) {
                    for (int cy = 0; cy < TEXT_CACHE_H; cy++) {
                        int screen_y = text_ty + cy;
                        const unsigned char *arow = text_alpha[percent] + cy * TEXT_CACHE_W;
                        for (int cx = 0; cx < TEXT_CACHE_W; cx++) {
                            unsigned char a = arow[cx];
                            if (!a) continue;
                            blend_px(text_blit_x + cx, screen_y, 255, 255, 255, a);
                        }
                    }
                }

                // 3) Double-buffering : envoi a l'ecran d'un seul memcpy.
                memcpy(fbp, back_buf, screensize);

                // 4) Lire le pourcentage reel
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

                // 5) TIMING ADAPTATIF : on cadence sur une horloge absolue plutot
                //    qu'un sleep fixe. Un sleep(33ms) fixe ajoute le temps de
                //    decodage+blit DEJA ecoule -> la video ralentit (chaque frame
                //    prend 33ms + travail). Ici on calcule l'instant theorique de
                //    la prochaine frame et on dort juste ce qu'il faut (ou rien si
                //    on est en retard) : le rythme reste ~30 fps stable.
                next_frame_ns += (long long)frame_delay_ms * 1000000LL;
                struct timespec now;
                clock_gettime(CLOCK_MONOTONIC, &now);
                long long now_ns = (long long)now.tv_sec * 1000000000LL + now.tv_nsec;
                long long wait_ns = next_frame_ns - now_ns;
                if (wait_ns > 0) {
                    struct timespec ts = { wait_ns / 1000000000LL, wait_ns % 1000000000LL };
                    nanosleep(&ts, NULL);
                } else if (wait_ns < -100000000LL) {
                    // On a plus de 100ms de retard (gros hoquet) : on resynchronise
                    // sur l'horloge courante pour ne pas accumuler la derive.
                    next_frame_ns = now_ns;
                }

                // Mode debug demande (Menu x5 + Enter) : on quitte l'animation.
                // L'image courante est deja liberee plus haut (ligne blit) : pas
                // de release ici (sinon double-release).
                if (g_debug_requested) goto debug_exit;
            }
        } while (more);

        de265_free_decoder(dctx);
        // on reboucle (loop infini)

        if (g_debug_requested) goto debug_exit;
    }

debug_exit:
    // Bascule vers la console des logs (tty3) : le splash disparait, on voit
    // l'auto-installer en direct.
    if (g_debug_requested) {
        munmap(fbp, screensize);   // liberer le framebuffer (plus d'affichage video)
        close(fbfd);
        enter_debug_console();     // KD_TEXT + activer tty3
        // IMPORTANT : le service a Restart=always. Si on quittait, systemd
        // relancerait fb_video et reprendrait l'ecran en boucle. On reste donc
        // vivant et inactif pour laisser la console des logs visible.
        while (1) pause();
    }

    // (jamais atteint en fonctionnement normal : boucle video infinie)
    for (int p = 0; p <= 100; p++) if (text_alpha[p]) free(text_alpha[p]);
    free(hevc_data);
    if (ttf_buffer) free(ttf_buffer);
    free(back_buf);
    munmap(fbp, screensize);
    close(fbfd);
    return 0;
}
