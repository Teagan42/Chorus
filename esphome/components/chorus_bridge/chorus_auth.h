#pragma once

// The v4 handshake's arithmetic (ADR-0074). Free of ESPHome on purpose, so
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
static const size_t KEY_SIZE = 32;    // the link key, and each session key
static const size_t NONCE_SIZE = 32;  // each side's, fresh per connection
static const size_t MAC_SIZE = 32;    // HMAC-SHA256, both proofs
// The host refuses a longer name; ESPHome's are at most 31 bytes.
static const size_t MAX_NAME_SIZE = 64;
// version, sample rate, bits, channels, then the device's nonce.
static const size_t HELLO_NONCE_OFFSET = 7;
static const size_t HELLO_FIXED_SIZE = HELLO_NONCE_OFFSET + NONCE_SIZE;
static const size_t MAX_HELLO_SIZE = HELLO_FIXED_SIZE + MAX_NAME_SIZE;
// The challenge's payload: the host's nonce, then its proof.
static const size_t CHALLENGE_SIZE = NONCE_SIZE + MAC_SIZE;

// Must match internal/bridge/auth.go byte for byte. Each proof opens with its
// role, so neither side's proof can be reflected as the other's.
static const char LINK_KEY_INFO[] = "chorus-bridge link v4";
static const char HOST_PROOF_CONTEXT[] = "chorus-bridge v4 host proof";
static const char DEVICE_PROOF_CONTEXT[] = "chorus-bridge v4 device proof";
static const char DEVICE_TO_HOST_INFO[] = "chorus-bridge v4 device to host";
static const char HOST_TO_DEVICE_INFO[] = "chorus-bridge v4 host to device";

inline bool hmac_sha256(const uint8_t *key, size_t key_len, const uint8_t *msg, size_t msg_len,
                        uint8_t out[MAC_SIZE]) {
  const mbedtls_md_info_t *md = mbedtls_md_info_from_type(MBEDTLS_MD_SHA256);
  return md != nullptr && mbedtls_md_hmac(md, key, key_len, msg, msg_len, out) == 0;
}

/// HKDF-SHA256 with a 32-byte output: one extract, one expand block
/// (RFC 5869). No salt means 32 zero bytes. `info` is a label without its
/// terminator; its length is bounded by the longest label above.
inline bool hkdf_sha256(const uint8_t *salt, size_t salt_len, const uint8_t *ikm, size_t ikm_len, const char *info,
                        size_t info_len, uint8_t out[KEY_SIZE]) {
  static const uint8_t zero_salt[MAC_SIZE] = {};
  uint8_t block[sizeof(DEVICE_TO_HOST_INFO)];  // the longest label, and the counter byte
  if (info_len + 1 > sizeof(block)) {
    return false;
  }
  if (salt == nullptr || salt_len == 0) {
    salt = zero_salt;
    salt_len = sizeof(zero_salt);
  }
  uint8_t prk[MAC_SIZE];
  std::memcpy(block, info, info_len);
  block[info_len] = 0x01;
  bool ok = hmac_sha256(salt, salt_len, ikm, ikm_len, prk) && hmac_sha256(prk, sizeof(prk), block, info_len + 1, out);
  mbedtls_platform_zeroize(prk, sizeof(prk));
  return ok;
}

/// HKDF-SHA256(psk, salt = none, info = LINK_KEY_INFO). A separate key, so
/// the link and the native API's Noise handshake never share one.
inline bool derive_link_key(const uint8_t psk[PSK_SIZE], uint8_t key[KEY_SIZE]) {
  return hkdf_sha256(nullptr, 0, psk, PSK_SIZE, LINK_KEY_INFO, sizeof(LINK_KEY_INFO) - 1, key);
}

/// The hello payload, big-endian, as internal/bridge/frame.go encodes it.
/// Returns its length, or 0 if the name does not fit.
inline size_t encode_hello(uint8_t version, uint32_t sample_rate, uint8_t bits, uint8_t channels,
                           const uint8_t nonce[NONCE_SIZE], const char *name, size_t name_len,
                           uint8_t out[MAX_HELLO_SIZE]) {
  if (name_len == 0 || name_len > MAX_NAME_SIZE) {
    return 0;
  }
  out[0] = version;
  out[1] = sample_rate >> 24;
  out[2] = (sample_rate >> 16) & 0xff;
  out[3] = (sample_rate >> 8) & 0xff;
  out[4] = sample_rate & 0xff;
  out[5] = bits;
  out[6] = channels;
  std::memcpy(out + HELLO_NONCE_OFFSET, nonce, NONCE_SIZE);
  std::memcpy(out + HELLO_FIXED_SIZE, name, name_len);
  return HELLO_FIXED_SIZE + name_len;
}

/// HMAC-SHA256(key, context || hello || host nonce). The name ends the
/// hello and the host nonce after it is fixed, so no length prefix is
/// needed. Fixed stack buffer: nothing here allocates.
inline bool compute_proof(const uint8_t key[KEY_SIZE], const char *context, size_t context_len, const uint8_t *hello,
                          size_t hello_len, const uint8_t host_nonce[NONCE_SIZE], uint8_t out[MAC_SIZE]) {
  uint8_t msg[sizeof(DEVICE_PROOF_CONTEXT) - 1 + MAX_HELLO_SIZE + NONCE_SIZE];
  if (context_len > sizeof(DEVICE_PROOF_CONTEXT) - 1 || hello_len > MAX_HELLO_SIZE) {
    return false;
  }
  size_t n = 0;
  std::memcpy(msg + n, context, context_len);
  n += context_len;
  std::memcpy(msg + n, hello, hello_len);
  n += hello_len;
  std::memcpy(msg + n, host_nonce, NONCE_SIZE);
  n += NONCE_SIZE;
  bool ok = hmac_sha256(key, KEY_SIZE, msg, n, out);
  mbedtls_platform_zeroize(msg, sizeof(msg));
  return ok;
}

inline bool host_proof(const uint8_t key[KEY_SIZE], const uint8_t *hello, size_t hello_len,
                       const uint8_t host_nonce[NONCE_SIZE], uint8_t out[MAC_SIZE]) {
  return compute_proof(key, HOST_PROOF_CONTEXT, sizeof(HOST_PROOF_CONTEXT) - 1, hello, hello_len, host_nonce, out);
}

inline bool device_proof(const uint8_t key[KEY_SIZE], const uint8_t *hello, size_t hello_len,
                         const uint8_t host_nonce[NONCE_SIZE], uint8_t out[MAC_SIZE]) {
  return compute_proof(key, DEVICE_PROOF_CONTEXT, sizeof(DEVICE_PROOF_CONTEXT) - 1, hello, hello_len, host_nonce,
                       out);
}

/// Constant time: a host's proof is compared without saying where it differs.
inline bool equal(const uint8_t *a, const uint8_t *b, size_t n) {
  uint8_t diff = 0;
  for (size_t i = 0; i < n; i++) {
    diff |= a[i] ^ b[i];
  }
  return diff == 0;
}

/// This connection's two AES-256-GCM keys: HKDF-SHA256 over the link key,
/// salted with the device's nonce then the host's, one info per direction.
inline bool derive_session_keys(const uint8_t key[KEY_SIZE], const uint8_t device_nonce[NONCE_SIZE],
                                const uint8_t host_nonce[NONCE_SIZE], uint8_t device_to_host[KEY_SIZE],
                                uint8_t host_to_device[KEY_SIZE]) {
  uint8_t salt[2 * NONCE_SIZE];
  std::memcpy(salt, device_nonce, NONCE_SIZE);
  std::memcpy(salt + NONCE_SIZE, host_nonce, NONCE_SIZE);
  return hkdf_sha256(salt, sizeof(salt), key, KEY_SIZE, DEVICE_TO_HOST_INFO, sizeof(DEVICE_TO_HOST_INFO) - 1,
                     device_to_host) &&
         hkdf_sha256(salt, sizeof(salt), key, KEY_SIZE, HOST_TO_DEVICE_INFO, sizeof(HOST_TO_DEVICE_INFO) - 1,
                     host_to_device);
}

}  // namespace esphome::chorus_bridge::auth
