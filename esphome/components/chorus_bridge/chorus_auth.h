#pragma once

// The v3 handshake's arithmetic (ADR-0066). Free of ESPHome on purpose, so
// esphome/test/chorus_auth_test.cpp compiles it on the host against the
// golden frames internal/bridge/auth_test.go checks the Go side with.
//
// mbedtls_md_hmac only. ESP-IDF builds mbedTLS without MBEDTLS_HKDF_C by
// default, and HKDF with a 32-byte output is two HMACs, so it is spelled out.

#include <cstddef>
#include <cstdint>
#include <cstring>

#include "mbedtls/md.h"
#include "mbedtls/platform_util.h"

namespace esphome::chorus_bridge::auth {

static const size_t PSK_SIZE = 32;    // api.encryption.key, decoded
static const size_t KEY_SIZE = 32;    // the derived link key
static const size_t NONCE_SIZE = 32;  // the host's challenge
static const size_t MAC_SIZE = 32;    // HMAC-SHA256
static const size_t HELLO_SIZE = 7;   // version, sample rate, bits, channels
// The host refuses a longer name; ESPHome's are at most 31 bytes.
static const size_t MAX_NAME_SIZE = 64;

// Must match internal/bridge/auth.go byte for byte.
static const char MAC_CONTEXT[] = "chorus-bridge v3";
static const char KEY_INFO[] = "chorus-bridge auth v3";

inline bool hmac_sha256(const uint8_t *key, size_t key_len, const uint8_t *msg, size_t msg_len,
                        uint8_t out[MAC_SIZE]) {
  const mbedtls_md_info_t *md = mbedtls_md_info_from_type(MBEDTLS_MD_SHA256);
  return md != nullptr && mbedtls_md_hmac(md, key, key_len, msg, msg_len, out) == 0;
}

/// HKDF-SHA256(psk, salt = none, info = KEY_INFO), 32 bytes: one extract,
/// one expand block (RFC 5869). A separate key, so the link and the native
/// API's Noise handshake never share one.
inline bool derive_link_key(const uint8_t psk[PSK_SIZE], uint8_t key[KEY_SIZE]) {
  static const uint8_t zero_salt[KEY_SIZE] = {};
  uint8_t prk[KEY_SIZE];
  uint8_t info[sizeof(KEY_INFO)];  // the terminator's byte holds the block counter
  std::memcpy(info, KEY_INFO, sizeof(KEY_INFO) - 1);
  info[sizeof(KEY_INFO) - 1] = 0x01;
  bool ok = hmac_sha256(zero_salt, sizeof(zero_salt), psk, PSK_SIZE, prk) &&
            hmac_sha256(prk, sizeof(prk), info, sizeof(info), key);
  mbedtls_platform_zeroize(prk, sizeof(prk));
  return ok;
}

/// HMAC-SHA256(key, MAC_CONTEXT || nonce || name || hello). The name is the
/// only variable-length field, so no length prefix is needed. Fixed stack
/// buffer: nothing here allocates.
inline bool compute_mac(const uint8_t key[KEY_SIZE], const uint8_t nonce[NONCE_SIZE], const char *name,
                        size_t name_len, const uint8_t hello[HELLO_SIZE], uint8_t mac[MAC_SIZE]) {
  if (name_len == 0 || name_len > MAX_NAME_SIZE) {
    return false;
  }
  uint8_t msg[sizeof(MAC_CONTEXT) - 1 + NONCE_SIZE + MAX_NAME_SIZE + HELLO_SIZE];
  size_t n = 0;
  std::memcpy(msg + n, MAC_CONTEXT, sizeof(MAC_CONTEXT) - 1);
  n += sizeof(MAC_CONTEXT) - 1;
  std::memcpy(msg + n, nonce, NONCE_SIZE);
  n += NONCE_SIZE;
  std::memcpy(msg + n, name, name_len);
  n += name_len;
  std::memcpy(msg + n, hello, HELLO_SIZE);
  n += HELLO_SIZE;
  return hmac_sha256(key, KEY_SIZE, msg, n, mac);
}

/// The hello payload, big-endian, as internal/bridge/frame.go encodes it.
inline void encode_hello(uint8_t version, uint32_t sample_rate, uint8_t bits, uint8_t channels,
                         uint8_t out[HELLO_SIZE]) {
  out[0] = version;
  out[1] = sample_rate >> 24;
  out[2] = (sample_rate >> 16) & 0xff;
  out[3] = (sample_rate >> 8) & 0xff;
  out[4] = sample_rate & 0xff;
  out[5] = bits;
  out[6] = channels;
}

}  // namespace esphome::chorus_bridge::auth
