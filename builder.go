package d2protocolparser

import (
	"os"

	"io"

	"bytes"

	"strings"

	"github.com/kelvyne/as3"
	"github.com/kelvyne/as3/bytecode"
	"github.com/kelvyne/swf"
)

// Protocol represents the Dofus 2 Protocol and contains
// every messages and types
type Protocol struct {
	Messages []Class
	Types    []Class
	Enums    []Enum
	Version  Version
}

// Enum represents a Dofus 2 Protocol Enumeration Class
type Enum struct {
	Name   string
	Values []EnumValue
}

// EnumValue represents a single Enumeration Values
type EnumValue struct {
	Name  string
	Value int32
}

// Class represents a Dofus 2 Protocol class
type Class struct {
	Name        string
	Namespace   string
	Parent      string
	Fields      []Field
	ProtocolID  uint16
	UseHashFunc bool
}

// Field represents a class field
type Field struct {
	Name        string
	Type        string
	WriteMethod string
	Method      string // Method contains the name of the method that should be used for scalar types

	IsVector          bool
	IsDynamicLength   bool
	Length            uint32
	WriteLengthMethod string

	UseTypeManager bool

	UseBBW      bool // Use BooleanByteWrapper
	BBWPosition uint
}

// Version represents a Dofus 2 Protocol version
type Version struct {
	Major    uint
	Minor    uint
	Release  uint
	Revision uint
	Patch    uint
}

type builder struct {
	abcFile *as3.AbcFile
}

func parseSwf(r io.ReadSeeker) (*swf.Swf, error) {
	s, err := swf.Parse(r)
	if err != nil {
		return nil, newError(err, "swf parsing failed")
	}
	return &s, nil
}

func parseAbc(s *swf.Swf) (*as3.AbcFile, error) {
	for _, tag := range s.Tags {
		if tag.Code() != swf.CodeTagDoABC {
			continue
		}
		doAbc := tag.(*swf.TagDoABC)
		if doAbc.Name != "frame1" {
			continue
		}

		abc, err := bytecode.Parse(bytecode.NewReader(bytes.NewReader(doAbc.ABCData)))
		if err != nil {
			return nil, newError(err, "abc parsing failed")
		}

		l, err := as3.Link(&abc)
		if err != nil {
			return nil, newError(err, "abc linking failed")
		}
		return &l, nil
	}
	return nil, newError(nil, "swf file does not contain frame1 tag")
}

// Build reads the DofusInvoker.swf at the given path and build a list of
// message and types
func Build(path string) (*Protocol, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	s, err := parseSwf(file)
	if err != nil {
		return nil, err
	}

	a, err := parseAbc(s)
	if err != nil {
		return nil, err
	}

	b := builder{abcFile: a}
	p, err := b.Build()
	if err != nil {
		return nil, newError(err, "protocol build failed")
	}

	if err = Verify(&p); err != nil {
		return nil, newError(err, "verification error")
	}
	return &p, nil
}

const (
	messagePrefix = "com.ankamagames.dofus.network.messages."
	typePrefix    = "com.ankamagames.dofus.network.types."
	enumPrefix    = "com.ankamagames.dofus.network.enums"
)

func (b *builder) Build() (Protocol, error) {
	var types []Class
	var messages []Class
	var enums []Enum
	for _, class := range b.abcFile.Classes {
		isMessage := strings.HasPrefix(class.Namespace, messagePrefix)
		isType := strings.HasPrefix(class.Namespace, typePrefix)
		if isType || isMessage {
			c, err := b.ExtractClass(class)
			if err != nil {
				return Protocol{}, err
			}
			switch {
			case isType:
				types = append(types, c)
			case isMessage:
				messages = append(messages, c)
			}
		} else if strings.HasPrefix(class.Namespace, enumPrefix) {
			e, err := b.ExtractEnum(class)
			if err != nil {
				return Protocol{}, err
			}
			enums = append(enums, e)
		}
	}
	v, err := b.ExtractVersion()
	if err != nil {
		return Protocol{}, err
	}
	return Protocol{messages, types, enums, v}, nil
}
