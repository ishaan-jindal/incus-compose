package incuscompose

// maxInterfaceNameLen is the maximum safe length for Linux interface names.
// While IFNAMSIZ allows 15 chars, some dhclient versions have bugs with names > 13.
// See: https://bugs.debian.org/cgi-bin/bugreport.cgi?bug=858580
const maxInterfaceNameLen = 13

// networkNamePrefix is used for hash-based short network names.
// "ic-" stands for incus-compose.
const networkNamePrefix = "ic-"

// networkNameHashLen is the number of base32 characters to use for the hash portion.
// 10 chars of base32 = 50 bits of entropy = ~1 quadrillion combinations.
const networkNameHashLen = 10

// maxContainerNameLen is the maximum length for Incus container names.
// Incus allows up to 63 characters, but we use 64 for the hash fallback.
const maxContainerNameLen = 63
