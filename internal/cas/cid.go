package cas

import "github.com/mexirica/strata/internal/cid"

type Algorithm = cid.Algorithm

const (
	AlgSHA256 = cid.AlgSHA256
	AlgBlake3 = cid.AlgBlake3
)

type CID = cid.CID

var (
	NewCID        = cid.NewCID
	ParseCID      = cid.ParseCID
	ParseCIDBytes = cid.ParseCIDBytes
)
