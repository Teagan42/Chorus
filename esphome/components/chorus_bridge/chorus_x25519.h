#pragma once

// X25519 for the v4 handshake's ephemeral exchange (ADR-0074), over
// mbedTLS's public ECDH API only, so it builds against mbedTLS 2.28 on the
// host and 3.6 under ESP-IDF 5.5, whose struct members are private. ESP-IDF
// builds MBEDTLS_ECP_C, MBEDTLS_ECDH_C and MBEDTLS_ECP_DP_CURVE25519_ENABLED
// by default. Free of ESPHome, so esphome/test/chorus_auth_test.cpp checks it
// against the golden frames and RFC 7748's vectors.

#include <cstddef>
#include <cstdint>
#include <cstring>

#include "mbedtls/bignum.h"
#include "mbedtls/ecdh.h"
#include "mbedtls/ecp.h"

namespace esphome::chorus_bridge::x25519 {

static const size_t KEY_SIZE = 32;

/// mbedTLS's RNG callback. The ladder uses it to blind its coordinates,
/// never for the result, so the output is the same whatever it returns.
using Rng = int (*)(void *, unsigned char *, size_t);

/// X25519(priv, peer) into out (RFC 7748). The scalar is clamped here.
/// False for a peer key of low order, which mbedTLS refuses by its list of
/// bad points, and for an all-zero result, checked again here.
inline bool shared(const uint8_t priv[KEY_SIZE], const uint8_t peer[KEY_SIZE], uint8_t out[KEY_SIZE], Rng rng,
                   void *rng_ctx) {
  mbedtls_ecp_group grp;
  mbedtls_ecp_point q;
  mbedtls_mpi d;
  mbedtls_mpi z;
  mbedtls_ecp_group_init(&grp);
  mbedtls_ecp_point_init(&q);
  mbedtls_mpi_init(&d);
  mbedtls_mpi_init(&z);
  // Clamped as RFC 7748 section 5 says, and as mbedtls_ecp_check_privkey
  // requires of a Montgomery scalar: the low three bits clear, bit 254 set.
  bool ok = mbedtls_ecp_group_load(&grp, MBEDTLS_ECP_DP_CURVE25519) == 0 &&
            mbedtls_mpi_read_binary_le(&d, priv, KEY_SIZE) == 0 && mbedtls_mpi_set_bit(&d, 0, 0) == 0 &&
            mbedtls_mpi_set_bit(&d, 1, 0) == 0 && mbedtls_mpi_set_bit(&d, 2, 0) == 0 &&
            mbedtls_mpi_set_bit(&d, 255, 0) == 0 && mbedtls_mpi_set_bit(&d, 254, 1) == 0 &&
            // Masks the top bit of u, as RFC 7748 section 5 says.
            mbedtls_ecp_point_read_binary(&grp, &q, peer, KEY_SIZE) == 0 &&
            mbedtls_ecdh_compute_shared(&grp, &z, &q, &d, rng, rng_ctx) == 0 &&
            mbedtls_mpi_write_binary_le(&z, out, KEY_SIZE) == 0;
  mbedtls_mpi_free(&z);
  mbedtls_mpi_free(&d);
  mbedtls_ecp_point_free(&q);
  mbedtls_ecp_group_free(&grp);
  if (!ok) {
    std::memset(out, 0, KEY_SIZE);
    return false;
  }
  uint8_t any = 0;
  for (size_t i = 0; i < KEY_SIZE; i++) {
    any |= out[i];
  }
  return any != 0;
}

/// The public key for priv: X25519(priv, 9), the base point's u.
inline bool public_key(const uint8_t priv[KEY_SIZE], uint8_t pub[KEY_SIZE], Rng rng, void *rng_ctx) {
  static const uint8_t base[KEY_SIZE] = {9};
  return shared(priv, base, pub, rng, rng_ctx);
}

}  // namespace esphome::chorus_bridge::x25519
