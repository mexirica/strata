package hasher

type Hasher interface {
	Hash(data []byte) [32]byte
	ToString(data []byte) string
}
