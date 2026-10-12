// sdr_capture.c — see sdr_capture.h.
//
// Capture sequence (all registers undocumented; names below are ours):
//   1. Force the modem's dump-engine clocks on.
//   2. Select the raw-ADC sample source and the 32-bit-per-sample layout.
//   3. Hand SRAM bank 2 (0x40840000..0x4085FFFF) from the CPU to the modem.
//   4. Arm the engine with a sample count, pulse the trigger, poll DONE.
//   5. Hand the bank back to the CPU and validate the buffer.
// The bank is reserved from the heap at link time (SOC_RESERVE_MEMORY_REGION),
// so nothing else can live there while the modem owns it.
#include "sdr_capture.h"
#include "sdkconfig.h"

#if defined(SDR_ENABLE) && SDR_ENABLE && CONFIG_IDF_TARGET_ESP32C6
#define SDR_HW 1
#else
#define SDR_HW 0
#endif

#if SDR_HW

#include <string.h>
#include "esp_event.h"
#include "esp_log.h"
#include "esp_rom_sys.h"
#include "esp_timer.h"
#include "esp_wifi.h"
#include "heap_memory_layout.h"
#include "soc/soc.h"

static const char *TAG = "sdr";

// --- SRAM bank handed to the dump engine ----------------------------------
#define SDR_BANK_START 0x40840000u
#define SDR_BANK_END   0x40860000u
SOC_RESERVE_MEMORY_REGION(SDR_BANK_START, SDR_BANK_END, sdr_iq_bank);

#define SDR_BUF        ((volatile uint32_t *)SDR_BANK_START)
#define SDR_GUARD_WORDS 4u
#define SDR_UNWRITTEN  0xA5A0055Au          // never a valid sample (bits 31..28 = 0xA)
#define SDR_GUARD(i)   (0x5A5AA5A5u ^ (i))

// --- Modem registers ---------------------------------------------------------
#define SRAM_OWNER_REG     0x60095004u      // bits 11..8: SRAM bank owner select
#define SRAM_OWNER_MASK    0x00000F00u
#define SRAM_OWNER_MODEM_B2 0x00000400u     // bank 2 -> modem

#define DUMP_CTRL_REG      0x600A9004u      // [17:0] count, 18 DONE, 19 TRIGGER, 31 ENABLE
#define DUMP_CTRL_DONE     (1u << 18)
#define DUMP_CTRL_TRIGGER  (1u << 19)
#define DUMP_CTRL_ENABLE   (1u << 31)
#define DUMP_SRC_REG       0x600A9008u      // [18:15] sample source
#define DUMP_SRC_MASK      0x00078000u
#define DUMP_SRC_RAW_ADC   (15u << 15)
#define DUMP_LAYOUT_REG    0x600A9014u      // [24:0] word packing layout
#define DUMP_LAYOUT_MASK   0x01FFFFFFu
#define DUMP_LAYOUT_IQ32   ((1u << 6) | (2u << 12) | (3u << 18))
#define RX_PATH_REG        0x600A20B4u      // bit 0 must be clear while dumping
#define CLK_FORCE_A_REG    0x600A9804u
#define CLK_FORCE_B_REG    0x600A980Cu
#define CLK_FORCE_C_REG    0x600A9814u

#define DUMP_TIMEOUT_US    20000
#define SDR_SETTLE_US      2000             // LO + AGC settle after a retune

// --- PHY library / ROM entry points (no public prototypes) -------------------
extern void chip_v7_set_chan(unsigned mhz, unsigned mode);   // libphy.a
extern void phy_set_freq(unsigned mhz, int offset_khz);      // libphy.a
extern void ram_stop_tx_tone(unsigned stop);                 // libphy.a
extern void ram_pbus_xpd_rx_on(unsigned on);                 // libphy.a
extern void pbus_workmode(void);                             // ROM
extern void rom_pbus_xpd_tx_off(void);                       // ROM
extern void set_rxclk_en(unsigned en);                       // ROM

// Linker-provided end of the static RAM image.
extern char _iram_end[], _data_end[], _bss_end[];

static bool s_active;
static const char *s_reason;

// Modem register values saved at begin() and restored at end(), so handing the
// radio back to 802.15.4 doesn't leave the dump clocks forced on.
static struct { uint32_t clk_a, clk_b, clk_c, src, layout, rx_path; } s_saved;

static inline void fence(void) { __asm__ volatile("fence rw,rw" ::: "memory"); }

bool sdr_capture_supported(void)
{
    if ((uintptr_t)_iram_end > SDR_BANK_START || (uintptr_t)_data_end > SDR_BANK_START ||
        (uintptr_t)_bss_end > SDR_BANK_START) {
        s_reason = "static RAM image overlaps the I/Q dump bank";
        return false;
    }
    s_reason = NULL;
    return true;
}

const char *sdr_capture_unsupported_reason(void)
{
    sdr_capture_supported();
    return s_reason ? s_reason : "";
}

bool sdr_capture_active(void) { return s_active; }

esp_err_t sdr_capture_begin(void)
{
    if (s_active) return ESP_OK;
    if (!sdr_capture_supported()) {
        ESP_LOGE(TAG, "unsupported: %s", s_reason);
        return ESP_ERR_NOT_SUPPORTED;
    }
    esp_err_t err = esp_event_loop_create_default();
    if (err != ESP_OK && err != ESP_ERR_INVALID_STATE) return err;

    wifi_init_config_t cfg = WIFI_INIT_CONFIG_DEFAULT();
    cfg.nvs_enable = 0;              // nothing to persist; keep NVS untouched
    cfg.static_rx_buf_num = 4;       // we never consume Wi-Fi frames — keep RAM use low
    if ((err = esp_wifi_init(&cfg)) != ESP_OK) {
        ESP_LOGE(TAG, "esp_wifi_init: %s", esp_err_to_name(err));
        return err;
    }
    esp_wifi_set_storage(WIFI_STORAGE_RAM);
    if ((err = esp_wifi_set_mode(WIFI_MODE_NULL)) != ESP_OK ||
        (err = esp_wifi_start()) != ESP_OK) {
        ESP_LOGE(TAG, "wifi start: %s", esp_err_to_name(err));
        esp_wifi_deinit();
        return err;
    }
    esp_wifi_set_ps(WIFI_PS_NONE);
    esp_wifi_set_promiscuous(true);  // keeps the RX chain powered without an association
    esp_wifi_set_channel(1, WIFI_SECOND_CHAN_NONE);

    s_saved.clk_a   = REG_READ(CLK_FORCE_A_REG);
    s_saved.clk_b   = REG_READ(CLK_FORCE_B_REG);
    s_saved.clk_c   = REG_READ(CLK_FORCE_C_REG);
    s_saved.src     = REG_READ(DUMP_SRC_REG);
    s_saved.layout  = REG_READ(DUMP_LAYOUT_REG);
    s_saved.rx_path = REG_READ(RX_PATH_REG);
    s_active = true;
    ESP_LOGI(TAG, "wideband receiver up (%u MS/s, %u-sample snapshots)",
             (unsigned)(SDR_SAMPLE_RATE_HZ / 1000000u), (unsigned)SDR_MAX_SAMPLES);
    return ESP_OK;
}

void sdr_capture_end(void)
{
    if (!s_active) return;
    REG_WRITE(DUMP_CTRL_REG, 0);
    REG_WRITE(CLK_FORCE_A_REG, s_saved.clk_a);
    REG_WRITE(CLK_FORCE_B_REG, s_saved.clk_b);
    REG_WRITE(CLK_FORCE_C_REG, s_saved.clk_c);
    REG_WRITE(DUMP_SRC_REG, s_saved.src);
    REG_WRITE(DUMP_LAYOUT_REG, s_saved.layout);
    REG_WRITE(RX_PATH_REG, s_saved.rx_path);
    esp_wifi_set_promiscuous(false);
    esp_wifi_stop();
    esp_wifi_deinit();
    s_active = false;
    ESP_LOGI(TAG, "wideband receiver down");
}

static bool is_wifi_center(uint16_t mhz)
{
    return (mhz >= 2412 && mhz <= 2472 && (mhz - 2412) % 5 == 0) || mhz == 2484;
}

bool sdr_capture_tune(uint16_t mhz)
{
    if (!s_active || mhz < SDR_TUNE_MIN_MHZ || mhz > SDR_TUNE_MAX_MHZ) return false;
    // The channel routine runs the PHY's per-channel calibration, but only
    // accepts Wi-Fi channel centers; for anything else calibrate on a nearby
    // channel and then program the synthesizer directly.
    if (is_wifi_center(mhz)) {
        chip_v7_set_chan(mhz, 0);
    } else {
        chip_v7_set_chan(2412, 0);
        phy_set_freq(mhz, 0);
    }
    // Receive-only: no TX tone, TX path powered down, RX path and clock on.
    ram_stop_tx_tone(1);
    pbus_workmode();
    rom_pbus_xpd_tx_off();
    ram_pbus_xpd_rx_on(1);
    set_rxclk_en(1);
    esp_rom_delay_us(SDR_SETTLE_US);
    return true;
}

bool sdr_capture_snapshot(uint32_t n, const uint32_t **out)
{
    if (!s_active || n == 0 || n > SDR_MAX_SAMPLES) return false;

    // Pre-fill with a pattern the engine can't produce so a short write is
    // detectable, plus guard words past the end to catch an overrun.
    for (uint32_t i = 0; i < n; i++) SDR_BUF[i] = SDR_UNWRITTEN;
    for (uint32_t i = 0; i < SDR_GUARD_WORDS; i++) SDR_BUF[n + i] = SDR_GUARD(i);

    REG_WRITE(CLK_FORCE_A_REG, 0xFFFFFFFFu);
    REG_WRITE(CLK_FORCE_C_REG, 0x0007FFFFu);
    REG_WRITE(CLK_FORCE_B_REG, 0xFFFFFFFFu);
    REG_WRITE(DUMP_CTRL_REG, 0);
    REG_WRITE(DUMP_LAYOUT_REG, (REG_READ(DUMP_LAYOUT_REG) & ~DUMP_LAYOUT_MASK) | DUMP_LAYOUT_IQ32);
    REG_WRITE(DUMP_SRC_REG, (REG_READ(DUMP_SRC_REG) & ~DUMP_SRC_MASK) | DUMP_SRC_RAW_ADC);
    REG_CLR_BIT(RX_PATH_REG, 1u);

    const uint32_t owner = REG_READ(SRAM_OWNER_REG);
    REG_WRITE(SRAM_OWNER_REG, (owner & ~SRAM_OWNER_MASK) | SRAM_OWNER_MODEM_B2);
    fence();
    (void)REG_READ(SRAM_OWNER_REG);

    // Clear any stale DONE, enable with the count, then pulse the trigger.
    REG_WRITE(DUMP_CTRL_REG, n | DUMP_CTRL_DONE);
    REG_WRITE(DUMP_CTRL_REG, n | DUMP_CTRL_ENABLE);
    REG_WRITE(DUMP_CTRL_REG, n | DUMP_CTRL_ENABLE | DUMP_CTRL_TRIGGER);
    REG_WRITE(DUMP_CTRL_REG, n | DUMP_CTRL_ENABLE);

    const int64_t deadline = esp_timer_get_time() + DUMP_TIMEOUT_US;
    bool done;
    do {
        done = (REG_READ(DUMP_CTRL_REG) & DUMP_CTRL_DONE) != 0;
    } while (!done && esp_timer_get_time() < deadline);

    REG_WRITE(DUMP_CTRL_REG, 0);
    REG_WRITE(SRAM_OWNER_REG, owner);
    fence();
    (void)REG_READ(SRAM_OWNER_REG);

    if (!done) return false;
    for (uint32_t i = 0; i < SDR_GUARD_WORDS; i++) {
        if (SDR_BUF[n + i] != SDR_GUARD(i)) return false;   // wrote past n
    }
    // A short write leaves the fill pattern behind; checking the tail is
    // enough since the engine writes sequentially.
    if (SDR_BUF[n - 1] == SDR_UNWRITTEN || SDR_BUF[0] == SDR_UNWRITTEN) return false;

    *out = (const uint32_t *)SDR_BUF;
    return true;
}

#else // !SDR_HW — stubs so callers needn't #if every use

bool sdr_capture_supported(void) { return false; }
const char *sdr_capture_unsupported_reason(void)
{
    return "firmware built without SDR support (needs SDR_ENABLE on an ESP32-C6)";
}
esp_err_t sdr_capture_begin(void) { return ESP_ERR_NOT_SUPPORTED; }
void sdr_capture_end(void) {}
bool sdr_capture_active(void) { return false; }
bool sdr_capture_tune(uint16_t mhz) { (void)mhz; return false; }
bool sdr_capture_snapshot(uint32_t n, const uint32_t **out) { (void)n; (void)out; return false; }

#endif // SDR_HW
