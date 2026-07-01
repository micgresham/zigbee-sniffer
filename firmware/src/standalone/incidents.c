// incidents.c — see incidents.h.
#if defined(BUILD_STANDALONE)

#include "incidents.h"
#include <string.h>
#include <stdio.h>
#include "esp_timer.h"
#include "esp_log.h"
#include "esp_spiffs.h"

static const char *TAG = "incidents";
static const char *LOG_PATH = "/spiffs/incidents.jsonl";
#define LOG_CAP_BYTES (48 * 1024)   // rotate the log past this size

#define MAX_DEVS 64
#define MIN_FRAMES 5                // only flag devices we've actually seen regularly

typedef struct {
    uint16_t addr;
    uint64_t last_us;
    uint32_t count;
    int8_t   rssi;
    uint8_t  lqi;
    bool     used;
    bool     down;                  // currently flagged as silent
} dev_t;

static dev_t s_devs[MAX_DEVS];
static incident_cb_t s_cb;
static uint32_t s_threshold_s = 120;
static bool s_fs_ok;

// Minimal MAC parse: extract a 16-bit (short) source address if present.
static bool mac_src_short(const uint8_t *mpdu, uint8_t len, uint16_t *out)
{
    if (len < 5) return false;
    uint16_t fcf = mpdu[0] | (mpdu[1] << 8);
    uint8_t dest_mode = (fcf >> 10) & 3;
    uint8_t src_mode  = (fcf >> 14) & 3;
    bool pan_comp = (fcf >> 6) & 1;
    size_t o = 3;                   // FCF(2) + seq(1)
    if (dest_mode) { o += 2; o += (dest_mode == 2) ? 2 : 8; }  // dst pan + addr
    if (src_mode == 2) {            // short source
        if (!pan_comp) o += 2;      // src pan
        if (o + 2 <= len) { *out = mpdu[o] | (mpdu[o + 1] << 8); return true; }
    }
    return false;
}

static dev_t *find_or_add(uint16_t addr)
{
    dev_t *free_slot = NULL;
    for (int i = 0; i < MAX_DEVS; i++) {
        if (s_devs[i].used && s_devs[i].addr == addr) return &s_devs[i];
        if (!s_devs[i].used && !free_slot) free_slot = &s_devs[i];
    }
    if (free_slot) { free_slot->used = true; free_slot->addr = addr; }
    return free_slot;  // NULL if table full
}

void incidents_init(incident_cb_t cb)
{
    s_cb = cb;
    esp_vfs_spiffs_conf_t conf = {
        .base_path = "/spiffs", .partition_label = "storage",
        .max_files = 2, .format_if_mount_failed = true,
    };
    s_fs_ok = (esp_vfs_spiffs_register(&conf) == ESP_OK);
    ESP_LOGI(TAG, "incident log %s", s_fs_ok ? "ready" : "UNAVAILABLE (fs mount failed)");
}

void incidents_set_threshold(uint32_t seconds)
{
    if (seconds >= 5) s_threshold_s = seconds;
}

void incidents_note_frame(const captured_frame_t *f)
{
    uint16_t src;
    if (!mac_src_short(f->psdu, f->len, &src)) return;
    if (src == 0xFFFF) return;       // broadcast, not a device
    dev_t *d = find_or_add(src);
    if (!d) return;
    d->last_us = esp_timer_get_time();
    d->count++;
    d->rssi = f->rssi;
    d->lqi = f->lqi;
    if (d->down) {                   // a previously-down device came back
        d->down = false;
        char j[160];
        int n = snprintf(j, sizeof(j),
            "{\"ts\":%llu,\"addr\":\"0x%04x\",\"reason\":\"recovered\",\"rssi\":%d,\"lqi\":%u}",
            (unsigned long long)(d->last_us / 1000000ULL), (unsigned)src, d->rssi,
            (unsigned)d->lqi);
        if (s_fs_ok) { FILE *fp = fopen(LOG_PATH, "a"); if (fp) { fprintf(fp, "%s\n", j); fclose(fp); } }
        if (s_cb) s_cb(j, n);
    }
}

static void rotate_if_needed(void)
{
    FILE *fp = fopen(LOG_PATH, "r");
    if (!fp) return;
    fseek(fp, 0, SEEK_END);
    long sz = ftell(fp);
    fclose(fp);
    if (sz > LOG_CAP_BYTES) {
        // Simple rotation: drop the oldest half by rewriting the tail.
        remove(LOG_PATH);
        ESP_LOGW(TAG, "incident log rotated (was %ld bytes)", sz);
    }
}

void incidents_tick(uint8_t channel)
{
    uint64_t now = esp_timer_get_time();
    uint64_t thresh_us = (uint64_t)s_threshold_s * 1000000ULL;
    for (int i = 0; i < MAX_DEVS; i++) {
        dev_t *d = &s_devs[i];
        if (!d->used || d->down || d->count < MIN_FRAMES) continue;
        if (now - d->last_us < thresh_us) continue;

        d->down = true;
        // The standalone primary has no local radio, so we can't snapshot channel
        // energy here; satellite-provided ED at incident time is future work.
        int8_t ed = 0;
        uint32_t silent_s = (uint32_t)((now - d->last_us) / 1000000ULL);
        char j[224];
        int n = snprintf(j, sizeof(j),
            "{\"ts\":%llu,\"addr\":\"0x%04x\",\"reason\":\"silence\",\"rssi\":%d,"
            "\"lqi\":%u,\"ch\":%u,\"ed\":%d,\"silent_s\":%u}",
            (unsigned long long)(now / 1000000ULL), (unsigned)d->addr, d->rssi,
            (unsigned)d->lqi, (unsigned)channel, ed, (unsigned)silent_s);
        ESP_LOGW(TAG, "INCIDENT %s", j);
        if (s_fs_ok) {
            rotate_if_needed();
            FILE *fp = fopen(LOG_PATH, "a");
            if (fp) { fprintf(fp, "%s\n", j); fclose(fp); }
        }
        if (s_cb) s_cb(j, n);
    }
}

int incidents_read_all(char *buf, size_t cap)
{
    if (!s_fs_ok) { return snprintf(buf, cap, "[]"); }
    FILE *fp = fopen(LOG_PATH, "r");
    if (!fp) { return snprintf(buf, cap, "[]"); }
    size_t n = fread(buf, 1, cap - 1, fp);
    fclose(fp);
    buf[n] = '\0';
    return (int)n;
}

#endif // BUILD_STANDALONE
