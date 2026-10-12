// The firmware's handshake and sealing arithmetic, compiled on the host
// against the same golden frames the Go side is checked with (ADR-0074). Run
// with `task firmware:test`; needs a C++17 compiler and mbedTLS.
//
// Usage: chorus_auth_test internal/bridge/testdata/handshake-v4.hex

#include <cstdint>
#include <cstdio>
#include <cstring>
#include <fstream>
#include <map>
#include <sstream>
#include <string>
#include <vector>

#include "chorus_auth.h"
#include "chorus_seal.h"

namespace auth = esphome::chorus_bridge::auth;
namespace seal = esphome::chorus_bridge::seal;

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

std::string hex(const std::vector<uint8_t> &v) { return hex(v.data(), v.size()); }

// A whole handshake frame, header included, as queue_plain_ lays it out.
std::vector<uint8_t> frame(uint8_t type, const uint8_t *payload, size_t n) {
  std::vector<uint8_t> out{type, 0, static_cast<uint8_t>(n >> 8), static_cast<uint8_t>(n & 0xff)};
  out.insert(out.end(), payload, payload + n);
  return out;
}

// A record as queue_frame_ builds one: room for the length, the frame, room
// for the tag; then sealed in place.
std::vector<uint8_t> sealed(seal::Sealer &s, uint8_t type, uint8_t flags, const std::vector<uint8_t> &payload) {
  std::vector<uint8_t> rec(seal::RECORD_HEADER_SIZE);
  rec.push_back(type);
  rec.push_back(flags);
  rec.insert(rec.end(), payload.begin(), payload.end());
  const size_t inner = rec.size() - seal::RECORD_HEADER_SIZE;
  rec.resize(rec.size() + seal::TAG_SIZE);
  if (!s.seal(rec.data(), inner)) {
    return {};
  }
  return rec;
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

void expect_true(const char *what, bool ok) { expect(what, ok ? "yes" : "no", "yes"); }

}  // namespace

int main(int argc, char **argv) {
  if (argc != 2) {
    std::fprintf(stderr, "usage: %s handshake-v4.hex\n", argv[0]);
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
  const std::vector<uint8_t> device_nonce = unhex(golden["device_nonce"]);
  const std::vector<uint8_t> host_nonce = unhex(golden["host_nonce"]);
  const std::string name = golden["name"];
  if (psk.size() != auth::PSK_SIZE || device_nonce.size() != auth::NONCE_SIZE ||
      host_nonce.size() != auth::NONCE_SIZE || name.empty()) {
    std::fprintf(stderr, "golden inputs are malformed\n");
    return 2;
  }

  // The living-room Satellite1's hello: v4, 16 kHz, 16-bit, both channels.
  uint8_t hello[auth::MAX_HELLO_SIZE];
  const size_t hello_len =
      auth::encode_hello(4, 16000, 16, 2, device_nonce.data(), name.data(), name.size(), hello);
  expect_true("hello encodes", hello_len == auth::HELLO_FIXED_SIZE + name.size());
  expect("hello", hex(frame(0x01, hello, hello_len)), golden["hello"]);

  uint8_t key[auth::KEY_SIZE];
  expect_true("derive_link_key", auth::derive_link_key(psk.data(), key));
  expect("link_key", hex(key, sizeof(key)), golden["link_key"]);

  // The device's check of the host: the golden challenge verifies, and a
  // proof that is the device's own, reflected, does not.
  const std::vector<uint8_t> challenge = unhex(golden["challenge"]);
  expect_true("challenge is a whole frame", challenge.size() == 4 + auth::CHALLENGE_SIZE && challenge[0] == 0x15);
  const uint8_t *nonce = challenge.data() + 4;
  const uint8_t *proof = nonce + auth::NONCE_SIZE;
  uint8_t want[auth::MAC_SIZE];
  expect_true("host_proof", auth::host_proof(key, hello, hello_len, nonce, want));
  expect("host_proof", hex(want, sizeof(want)), golden["host_proof"]);
  expect_true("the host's proof verifies", auth::equal(proof, want, auth::MAC_SIZE));

  uint8_t mine[auth::MAC_SIZE];
  expect_true("device_proof", auth::device_proof(key, hello, hello_len, nonce, mine));
  expect("device_proof", hex(mine, sizeof(mine)), golden["device_proof"]);
  expect("auth", hex(frame(0x06, mine, sizeof(mine))), golden["auth"]);
  expect_true("a reflected device proof is not the host's", !auth::equal(mine, want, auth::MAC_SIZE));

  // A rewritten hello breaks the host's proof: one channel instead of two.
  uint8_t rewritten[auth::MAX_HELLO_SIZE];
  std::memcpy(rewritten, hello, hello_len);
  rewritten[6] = 1;
  uint8_t other[auth::MAC_SIZE];
  auth::host_proof(key, rewritten, hello_len, nonce, other);
  expect_true("a proof over a rewritten hello does not verify", !auth::equal(proof, other, auth::MAC_SIZE));

  uint8_t up[auth::KEY_SIZE], down[auth::KEY_SIZE];
  expect_true("derive_session_keys", auth::derive_session_keys(key, device_nonce.data(), nonce, up, down));
  expect("key_device_to_host", hex(up, sizeof(up)), golden["key_device_to_host"]);
  expect("key_host_to_device", hex(down, sizeof(down)), golden["key_host_to_device"]);

  // The device seals the uplink: a mic chunk, then a mute report.
  seal::Sealer tx;
  expect_true("tx keyed", tx.set_key(up));
  expect("uplink_0", hex(sealed(tx, 0x02, 0, {1, 2, 3, 4})), golden["uplink_0"]);
  expect("uplink_1", hex(sealed(tx, 0x05, 1, {})), golden["uplink_1"]);

  // And opens the downlink: a TTS chunk, then a stop tagged 7.
  seal::Sealer rx;
  expect_true("rx keyed", rx.set_key(down));
  std::vector<uint8_t> d0 = unhex(golden["downlink_0"]);
  std::vector<uint8_t> d1 = unhex(golden["downlink_1"]);
  std::vector<uint8_t> d0_copy = d0;
  expect_true("downlink_0 opens", rx.open(d0.data(), d0.size()));
  expect("downlink_0 frame", hex(d0.data() + 2, d0.size() - 2 - seal::TAG_SIZE), "10000a0b0c0d");
  expect_true("downlink_1 opens", rx.open(d1.data(), d1.size()));
  expect("downlink_1 frame", hex(d1.data() + 2, d1.size() - 2 - seal::TAG_SIZE), "1107");

  // Replayed: the first record again, now under counter 2.
  std::vector<uint8_t> replay = d0_copy;
  expect_true("a replayed record does not open", !rx.open(replay.data(), replay.size()));

  // Reordered: the second record first.
  {
    seal::Sealer fresh;
    fresh.set_key(down);
    std::vector<uint8_t> second = unhex(golden["downlink_1"]);
    expect_true("a reordered record does not open", !fresh.open(second.data(), second.size()));
  }
  // Tampered: each of the type, a payload byte, the tag and the length.
  for (size_t at : {size_t{2}, size_t{4}, d0_copy.size() - 1, size_t{1}}) {
    seal::Sealer fresh;
    fresh.set_key(down);
    std::vector<uint8_t> bad = d0_copy;
    bad[at] ^= 0x01;
    std::string what = "a record altered at byte " + std::to_string(at) + " does not open";
    expect_true(what.c_str(), !fresh.open(bad.data(), bad.size()));
  }
  // Reflected: the device's own uplink record, opened as downlink.
  {
    seal::Sealer fresh;
    fresh.set_key(down);
    std::vector<uint8_t> mirror = unhex(golden["uplink_0"]);
    expect_true("an uplink record reflected does not open", !fresh.open(mirror.data(), mirror.size()));
  }
  // Truncated, and too short to hold a tag.
  {
    seal::Sealer fresh;
    fresh.set_key(down);
    std::vector<uint8_t> cut = d0_copy;
    expect_true("a truncated record does not open", !fresh.open(cut.data(), cut.size() - 1));
    uint8_t tiny[3] = {0x00, 0x01, 0x10};
    expect_true("a record shorter than a tag does not open", !fresh.open(tiny, sizeof(tiny)));
  }
  // A reset forgets the key, as disconnect_ does.
  rx.reset();
  std::vector<uint8_t> again = d0_copy;
  expect_true("a reset sealer opens nothing", !rx.keyed() && !rx.open(again.data(), again.size()));

  // The bounds the firmware relies on to keep its buffers fixed.
  uint8_t scratch[auth::MAX_HELLO_SIZE];
  const std::string too_long(auth::MAX_NAME_SIZE + 1, 'a');
  expect_true("empty name refused", auth::encode_hello(4, 16000, 16, 2, device_nonce.data(), "", 0, scratch) == 0);
  expect_true("long name refused", auth::encode_hello(4, 16000, 16, 2, device_nonce.data(), too_long.data(),
                                                      too_long.size(), scratch) == 0);

  return failures == 0 ? 0 : 1;
}
