package config

import (
	"github.com/ebay/releaser/pkg/util/strvals"
)

func (v ValueType) Func() func(string, map[string]interface{}) error {
	switch v {
	case ValueTypeRaw:
		return strvals.ParseInto
	case ValueTypeStr:
		return strvals.ParseIntoString
	default:
		return strvals.ParseInto
	}
}
