#pragma once

// Sealed frames (ADR-0074). After the handshake every frame is a record:
//
//   length:u16  AES-256-GCM(type:u8 flags:u8 payload)  tag[16]
//
// under one key per direction, with a nonce of four zero bytes and a
// big-endian u64 counter that never travels. The length is the additional
// data. Free of ESPHome, so esphome/test/chorus_auth_test.cpp checks it on the
// host against the golden records internal/bridge/seal.go is checked with.
//
// AES-GCM, not ChaCha20-Poly1305: ESP-IDF 5.5 builds MBEDTLS_GCM_C by default
// and the ESP32-S3 has AES in hardware (MBEDTLS_GCM_ALT routes these calls to
// esp_aes_gcm.c); MBEDTLS_CHACHAPOLY_C defaults to off.

#include <cstddef>
#include <cstdint>
#include <cstring>

#include "mbedtls/gcm.h"

namespace esphome::chorus_bridge::seal {

static const size_t KEY_SIZE = 32;
static const size_t TAG_SIZE = 16;
static const size_t RECORD_HEADER_SIZE = 2;  // the length; type and flags are sealed
static const size_t INNER_HEADER_SIZE = 2;   // type, flags
// What a sealed frame costs beyond its payload. A v3 frame's header was 4.
static const size_t OVERHEAD = RECORD_HEADER_SIZE + INNER_HEADER_SIZE + TAG_SIZE;
static const size_t NONCE_SIZE = 12;

/// One direction of a link: a key and its counter. Not copyable, because a
/// copy would seal two records under one counter.
class Sealer {
 public:
  Sealer() { mbedtls_gcm_init(&this->ctx_); }
  ~Sealer() { mbedtls_gcm_free(&this->ctx_); }
  Sealer(const Sealer &) = delete;
  Sealer &operator=(const Sealer &) = delete;

  /// Keys a new connection, counter from zero.
  bool set_key(const uint8_t key[KEY_SIZE]) {
    this->reset();
    this->keyed_ = mbedtls_gcm_setkey(&this->ctx_, MBEDTLS_CIPHER_ID_AES, key, KEY_SIZE * 8) == 0;
    return this->keyed_;
  }

  /// Forgets the key: free wipes the key schedule.
  void reset() {
    mbedtls_gcm_free(&this->ctx_);
    mbedtls_gcm_init(&this->ctx_);
    this->seq_ = 0;
    this->keyed_ = false;
  }

  bool keyed() const { return this->keyed_; }

  /// Seals in place. `record` holds RECORD_HEADER_SIZE bytes of room, then
  /// `inner_len` bytes of type, flags and payload, then TAG_SIZE bytes of
  /// room; the length and the tag are written here. The counter is spent
  /// whether or not this succeeds.
  bool seal(uint8_t *record, size_t inner_len) {
    const size_t sealed_len = inner_len + TAG_SIZE;
    if (!this->keyed_ || inner_len < INNER_HEADER_SIZE || sealed_len > 0xffff || this->seq_ == UINT64_MAX) {
      return false;
    }
    record[0] = sealed_len >> 8;
    record[1] = sealed_len & 0xff;
    uint8_t nonce[NONCE_SIZE];
    this->next_nonce_(nonce);
    uint8_t *inner = record + RECORD_HEADER_SIZE;
    return mbedtls_gcm_crypt_and_tag(&this->ctx_, MBEDTLS_GCM_ENCRYPT, inner_len, nonce, sizeof(nonce), record,
                                     RECORD_HEADER_SIZE, inner, inner, TAG_SIZE, inner + inner_len) == 0;
  }

  /// Opens a whole record in place: on success the type, flags and payload
  /// sit at record + RECORD_HEADER_SIZE, record_len - RECORD_HEADER_SIZE -
  /// TAG_SIZE bytes of them.
  /// False is a record that was tampered with, replayed, reordered or sealed
  /// under another key; the link must be dropped, as the counter is spent.
  ///
  /// In place as mbedTLS's own TLS record layer decrypts GCM: gcm.h's note
  /// against it is stricter than gcm.c, and ESP-IDF's esp_aes_gcm.c, which
  /// refuse only an output that starts ahead of its input.
  bool open(uint8_t *record, size_t record_len) {
    if (!this->keyed_ || record_len < OVERHEAD || this->seq_ == UINT64_MAX) {
      return false;
    }
    const size_t sealed_len = (static_cast<size_t>(record[0]) << 8) | record[1];
    if (sealed_len != record_len - RECORD_HEADER_SIZE) {
      return false;
    }
    const size_t inner_len = sealed_len - TAG_SIZE;
    uint8_t nonce[NONCE_SIZE];
    this->next_nonce_(nonce);
    uint8_t *inner = record + RECORD_HEADER_SIZE;
    return mbedtls_gcm_auth_decrypt(&this->ctx_, inner_len, nonce, sizeof(nonce), record, RECORD_HEADER_SIZE,
                                    inner + inner_len, TAG_SIZE, inner, inner) == 0;
  }

 protected:
  void next_nonce_(uint8_t nonce[NONCE_SIZE]) {
    std::memset(nonce, 0, 4);
    const uint64_t seq = this->seq_++;
    for (int i = 0; i < 8; i++) {
      nonce[4 + i] = (seq >> (56 - 8 * i)) & 0xff;
    }
  }

  mbedtls_gcm_context ctx_;
  uint64_t seq_{0};
  bool keyed_{false};
};

}  // namespace esphome::chorus_bridge::seal
