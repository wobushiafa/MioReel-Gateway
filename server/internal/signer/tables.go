package signer

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

//go:embed tables.json
var rawTablesJSON []byte

type rawTablesFile struct {
	Third       []ThirdProfile `json:"third"`
	LiteralRows []string       `json:"literalRows"`
	TableData   []struct {
		Selector int      `json:"selector"`
		Rows     []string `json:"rows"`
	} `json:"tableData"`
	Algorithm struct {
		CipherProfiles []rawCipherProfile `json:"cipherProfiles"`
		Planes         []struct {
			Plane                int            `json:"plane"`
			OutputBitToSourceBit map[string]int `json:"outputBitToSourceBit"`
		} `json:"planes"`
	} `json:"algorithm"`
}

type rawCipherProfile struct {
	KeyExpansionScalar                              string  `json:"keyExpansionScalar"`
	PermutationDerivedFromOriginalInitialState      [4]int  `json:"permutationDerivedFromOriginalInitialState"`
	SubShiftDerivedFromOriginalWriteStates          [16]int `json:"subShiftDerivedFromOriginalWriteStates"`
	MiddleColumnPermutationDerivedFromOriginalWrite [4]int  `json:"middleColumnPermutationDerivedFromOriginalWriteStates"`
	PublicSubstitutionHex                           string  `json:"publicSubstitutionHex"`
}

// ThirdProfile describes one profile for thirdHead (selectors 1, 4, 7, 10, 13).
type ThirdProfile struct {
	Selector                           int    `json:"selector"`
	PublicScalar                       uint64 `json:"publicScalar"`
	FirstBlockInputPermutation         [8]int `json:"firstBlockInputPermutation"`
	PaddedBlockKeyHashPermutation      [8]int `json:"paddedBlockKeyHashPermutation"`
	PaddedBlockLengthXorCanonicalIndex int    `json:"paddedBlockLengthXorCanonicalIndex"`
}

// CipherProfile describes one custom block cipher profile rotated by epoch & 3.
type CipherProfile struct {
	KeyExpansionScalar uint32
	Perm               [4]int
	Shift              [16]int
	Middle             [4]int
	Box                [256]byte
}

// PlaneMapping stores output bit (0..7) -> source bit (0..63) for extract/embed.
type PlaneMapping struct {
	OutputToSource [8]int
}

type embeddedTables struct {
	ThirdProfiles  map[int]ThirdProfile
	Tables         map[int][8][256]uint64
	LiteralRows    []uint64
	CipherProfiles [4]CipherProfile
	Planes         [8]PlaneMapping
}

var tables = mustLoadTables()

func mustLoadTables() embeddedTables {
	var raw rawTablesFile
	if err := json.Unmarshal(rawTablesJSON, &raw); err != nil {
		panic(fmt.Sprintf("signer: invalid embedded tables.json: %v", err))
	}

	out := embeddedTables{
		ThirdProfiles: make(map[int]ThirdProfile, len(raw.Third)),
		Tables:        make(map[int][8][256]uint64, len(raw.TableData)),
		LiteralRows:   make([]uint64, len(raw.LiteralRows)),
	}

	for _, p := range raw.Third {
		out.ThirdProfiles[p.Selector] = p
	}

	for i, s := range raw.LiteralRows {
		v, err := parseHexU64(s)
		if err != nil {
			panic(fmt.Sprintf("signer: invalid literalRows[%d]: %v", i, err))
		}
		out.LiteralRows[i] = v
	}

	for _, td := range raw.TableData {
		if len(td.Rows) != 8 {
			panic(fmt.Sprintf("signer: tableData[%d] rows=%d != 8", td.Selector, len(td.Rows)))
		}
		var mat [8][256]uint64
		for rIdx, rowHex := range td.Rows {
			b, err := hex.DecodeString(rowHex)
			if err != nil || len(b) != 256*8 {
				panic(fmt.Sprintf("signer: tableData[%d][%d] invalid len=%d err=%v", td.Selector, rIdx, len(b), err))
			}
			for j := 0; j < 256; j++ {
				off := j * 8
				// bigRead(bytes, j*8, little=true) -> standard LE uint64
				mat[rIdx][j] = uint64(b[off]) |
					uint64(b[off+1])<<8 |
					uint64(b[off+2])<<16 |
					uint64(b[off+3])<<24 |
					uint64(b[off+4])<<32 |
					uint64(b[off+5])<<40 |
					uint64(b[off+6])<<48 |
					uint64(b[off+7])<<56
			}
		}
		out.Tables[td.Selector] = mat
	}

	if len(raw.Algorithm.CipherProfiles) != 4 {
		panic(fmt.Sprintf("signer: expected 4 cipherProfiles, got %d", len(raw.Algorithm.CipherProfiles)))
	}
	for i, cp := range raw.Algorithm.CipherProfiles {
		scalar, err := parseHexU64(cp.KeyExpansionScalar)
		if err != nil {
			panic(fmt.Sprintf("signer: invalid cipherProfiles[%d].keyExpansionScalar: %v", i, err))
		}
		boxBytes, err := hex.DecodeString(cp.PublicSubstitutionHex)
		if err != nil || len(boxBytes) != 256 {
			panic(fmt.Sprintf("signer: invalid cipherProfiles[%d].publicSubstitutionHex", i))
		}
		var box [256]byte
		copy(box[:], boxBytes)
		out.CipherProfiles[i] = CipherProfile{
			KeyExpansionScalar: uint32(scalar),
			Perm:               cp.PermutationDerivedFromOriginalInitialState,
			Shift:              cp.SubShiftDerivedFromOriginalWriteStates,
			Middle:             cp.MiddleColumnPermutationDerivedFromOriginalWrite,
			Box:                box,
		}
	}

	if len(raw.Algorithm.Planes) != 8 {
		panic(fmt.Sprintf("signer: expected 8 planes, got %d", len(raw.Algorithm.Planes)))
	}
	for i, pl := range raw.Algorithm.Planes {
		var pm PlaneMapping
		for d := 0; d < 8; d++ {
			s, ok := pl.OutputBitToSourceBit[strconv.Itoa(d)]
			if !ok {
				panic(fmt.Sprintf("signer: plane %d missing bit %d", i, d))
			}
			pm.OutputToSource[d] = s
		}
		out.Planes[i] = pm
	}

	return out
}

func parseHexU64(s string) (uint64, error) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "0x"), "0X")
	return strconv.ParseUint(s, 16, 64)
}
