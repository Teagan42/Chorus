// The firmware's handshake arithmetic, compiled on the host against the same
// golden frames the Go side is checked with (ADR-0066). Run with
// `task firmware:test`; needs a C++17 compiler and mbedTLS's headers.
//
// Usage: chorus_auth_test internal/bridge/testdata/handshake-v3.hex

#include <cstdint>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <map>
#include <sstream>
#include <string>
#include <vector>

#include "chorus_auth.h"

namespace auth = esphome::chorus_bridge::auth;

namespace {

std::vector<uint8_t> unhex(const std::string &s) {
  std::vector<uint8_t> out;
  for (size_t i = 0; i + 1 < s.size(); i += 2) {
    out.push_back(static_cast<uint8_t>(std::stoul(s.substr(i, 2), nullptr, 16)));
  }
  return out;
}

std::string hex(const uint8_t *p, size_t n) {
  static const char digits[] = "0123456789abcdef";
  std::string out;
  for (size_t i = 0; i < n; i++) {
    out.push_back(digits[p[i] >> 4]);
    out.push_back(digits[p[i] & 0xf]);
  }
  return out;
}

// A whole frame, header included, as the firmware's queue_frame_ lays it out.
std::vector<uint8_t> frame(uint8_t type, const uint8_t *payload, size_t n) {
  std::vector<uint8_t> out{type, 0, static_cast<uint8_t>(n >> 8), static_cast<uint8_t>(n & 0xff)};
  out.insert(out.end(), payload, payload + n);
  return out;
}

int failures = 0;

void expect(const char *what, const std::string &got, const std::string &want) {
  if (got != want) {
    std::fprintf(stderr, "FAIL %s\n  got  %s\n  want %s\n", what, got.c_str(), want.c_str());
    failures++;
  } else {
    std::printf("ok   %s\n", what);
  }
}

}  // namespace

int main(int argc, char **argv) {
  if (argc != 2) {
    std::fprintf(stderr, "usage: %s handshake-v3.hex\n", argv[0]);
    return 2;
  }
  std::ifstream in(argv[1]);
  if (!in) {
    std::fprintf(stderr, "cannot open %s\n", argv[1]);
    return 2;
  }
  std::map<std::string, std::string> golden;
  std::string line;
  while (std::getline(in, line)) {
    if (line.empty() || line[0] == '#') {
      continue;
    }
    std::istringstream fields(line);
    std::string key, value;
    fields >> key >> value;
    golden[key] = value;
  }

  const std::vector<uint8_t> psk = unhex(golden["psk"]);
  const std::vector<uint8_t> nonce = unhex(golden["nonce"]);
  const std::string name = golden["name"];
  if (psk.size() != auth::PSK_SIZE || nonce.size() != auth::NONCE_SIZE || name.empty()) {
    std::fprintf(stderr, "golden inputs are malformed\n");
    return 2;
  }

  // The living-room Satellite1's hello: 16 kHz, 16-bit, both channels.
  uint8_t hello[auth::HELLO_SIZE];
  auth::encode_hello(3, 16000, 16, 2, hello);
  std::vector<uint8_t> hello_frame = frame(0x01, hello, sizeof(hello));
  expect("hello", hex(hello_frame.data(), hello_frame.size()), golden["hello"]);

  std::vector<uint8_t> challenge = frame(0x15, nonce.data(), nonce.size());
  expect("challenge", hex(challenge.data(), challenge.size()), golden["challenge"]);

  uint8_t key[auth::KEY_SIZE];
  if (!auth::derive_link_key(psk.data(), key)) {
    std::fprintf(stderr, "FAIL derive_link_key returned false\n");
    return 1;
  }
  expect("link_key", hex(key, sizeof(key)), golden["link_key"]);

  uint8_t payload[auth::MAC_SIZE + auth::MAX_NAME_SIZE];
  if (!auth::compute_mac(key, nonce.data(), name.data(), name.size(), hello, payload)) {
    std::fprintf(stderr, "FAIL compute_mac returned false\n");
    return 1;
  }
  expect("mac", hex(payload, auth::MAC_SIZE), golden["mac"]);

  std::memcpy(payload + auth::MAC_SIZE, name.data(), name.size());
  std::vector<uint8_t> answer = frame(0x06, payload, auth::MAC_SIZE + name.size());
  expect("auth", hex(answer.data(), answer.size()), golden["auth"]);

  // The bounds the firmware relies on to keep its buffers fixed.
  uint8_t scratch[auth::MAC_SIZE];
  const std::string too_long(auth::MAX_NAME_SIZE + 1, 'a');
  expect("empty name refused", auth::compute_mac(key, nonce.data(), "", 0, hello, scratch) ? "accepted" : "refused",
         "refused");
  expect("long name refused",
         auth::compute_mac(key, nonce.data(), too_long.data(), too_long.size(), hello, scratch) ? "accepted"
                                                                                                : "refused",
         "refused");

  return failures == 0 ? 0 : 1;
}
