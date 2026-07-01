// incidents.h — on-device device tracking + silence-based incident detection,
// persisted to flash (SPIFFS). This is the lightweight firmware "trigger" tier;
// the browser does richer live analysis. Core goal: log dropouts even with no
// browser/host connected (see docs/incidents.md).
#pragma once

#include <stddef.h>
#include <stdint.h>
#include "proto.h"

#ifdef __cplusplus
extern "C" {
#endif

// Called when an incident is raised, with a UTF-8 JSON string (for WS push).
typedef void (*incident_cb_t)(const char *json, size_t len);

void incidents_init(incident_cb_t cb);

// Update the device table from a captured frame (extracts MAC short src addr).
void incidents_note_frame(const captured_frame_t *f);

// Periodic check for silent devices; raises + logs incidents. `channel` is the
// current capture channel (used to snapshot channel energy in the incident).
void incidents_tick(uint8_t channel);

// Configurable silence threshold (seconds) before a device is flagged.
void incidents_set_threshold(uint32_t seconds);

// Read the persisted incident log (JSON lines) into buf; returns bytes written.
int incidents_read_all(char *buf, size_t cap);

#ifdef __cplusplus
}
#endif
