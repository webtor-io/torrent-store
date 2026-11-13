package privaterecord

import "encoding/hex"

// Vector pins the record format: torrent-store's package and server tests both run it.
var Vector = struct {
	Key, Nonce []byte
	Hash, UID  string
	Tiers      [][]string
	Blob       []byte
	Hosts      []string
}{
	Key:   mustHex("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"),
	Nonce: mustHex("000102030405060708090a0b"),
	Hash:  "0123456789abcdef0123456789abcdef01234567",
	UID:   "89abcdef0123456789abcdef0123456789abcdef",
	Tiers: [][]string{
		{"https://u0000.tracker.example/announce?passkey=00000000000000000000000000000000&k=0"},
		{"udp://tracker.example:6969/announce"},
	},
	Blob:  mustHex("000102030405060708090a0b3c20a039ffd4ee39f963add0eacb1019f7a6f40edf542a4c0857d5ab691b61d16a75dcd2cab973f504c81ac2e9e946579b3703e865a6c2a94cfc4f6025d3c5dec00cf64ae3e116512c649a5edfbf3ed808baf95246536358cdcdaefc48d6d2b0fbda04caff63bd7eee26119d3c07401cd102108586bd44f73d9332f7b4df583794336ea8dba2a5306dc315fc40ec2a8927126c5fe207a96ae50ac02366"), // 169 bytes
	Hosts: []string{"tracker.example"},
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
