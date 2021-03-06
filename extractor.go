package d2protocolparser

import (
	"fmt"
	"strconv"
	"strings"

	"errors"

	"github.com/kelvyne/as3"
	"github.com/kelvyne/as3/bytecode"
)

// ErrExtractNoProtocolID means that the protocolId trait could not be found
// when extracting class
var ErrExtractNoProtocolID = errors.New("no protocolId found")

// ErrExtractProtocolIDNotConst means that the protocolId trait is not a const trait
var ErrExtractProtocolIDNotConst = errors.New("protocolId not a const trait")

// ErrExtractProtocolIDNotInt means that the protocolId trait is not an integer
var ErrExtractProtocolIDNotInt = errors.New("protocolId not an int trait")

// ErrExtractNoBuildInfos means that the class BuildInfos was not found
var ErrExtractNoBuildInfos = errors.New("no BuildInfos found")

func (b *builder) ExtractEnum(class as3.Class) (Enum, error) {
	var values []EnumValue
	for _, trait := range class.ClassTraits.Slots {
		if trait.Source.VKind != bytecode.SlotKindInt {
			return Enum{}, fmt.Errorf("enumeration value %v of %v is not an uint", trait.Name, class.Name)
		}
		name := trait.Name
		value := b.abcFile.Source.ConstantPool.Integers[trait.Source.VIndex]
		values = append(values, EnumValue{name, value})
	}
	return Enum{class.Name, values}, nil
}

func (b *builder) ExtractClass(class as3.Class) (Class, error) {
	trait, found := findMethodWithPrefix(class, "serializeAs_")
	if !found {
		return Class{}, fmt.Errorf("serialize method not found in class %v", class.Name)
	}

	m := b.abcFile.Methods[trait.Method]
	if err := m.BodyInfo.Disassemble(); err != nil {
		return Class{}, fmt.Errorf("failed to disassemble %v", class.Name)
	}

	fields, err := b.extractMessageFields(class)
	if err != nil {
		return Class{}, fmt.Errorf("failed retrieve %v's fields", class.Name)
	}

	fieldMap := map[string]*Field{}
	for i, f := range fields {
		fieldMap[f.Name] = &fields[i]
	}

	if err = b.extractSerializeMethods(class, m, fieldMap); err != nil {
		return Class{}, err
	}

	for i := range fields {
		reduceType(&fields[i])
		reduceMethod(&fields[i])
	}

	protocolID, err := b.extractProtocolID(class)
	if err != nil {
		return Class{}, err
	}

	useHashFunc, err := b.extractUseHashFunc(class)

	superName := class.SuperName
	if superName == "Object" || superName == "NetworkMessage" {
		superName = ""
	}
	return Class{class.Name, class.Namespace, superName, fields, protocolID, useHashFunc}, nil
}

func (b *builder) extractUseHashFunc(class as3.Class) (bool, error) {
	getPackFunc := func(c as3.Class) (bool, as3.Method) {
		for _, m := range c.InstanceTraits.Methods {
			if m.Name == "pack" {
				return true, b.abcFile.Methods[m.Source.Method]
			}
		}
		return false, as3.Method{}
	}
	f, m := getPackFunc(class)
	if !f {
		return false, nil
	}
	if err := m.BodyInfo.Disassemble(); err != nil {
		return false, fmt.Errorf("could not disassemble pack method: %v", err)
	}

	for _, instr := range m.BodyInfo.Instructions {
		if instr.Model.Name == "getlex" {
			multiname := b.abcFile.Source.ConstantPool.Multinames[instr.Operands[0]]
			if multiname.Kind == bytecode.MultinameKindQName {
				name := b.abcFile.Source.ConstantPool.Strings[multiname.Name]
				if name == "HASH_FUNCTION" {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (b *builder) extractProtocolID(class as3.Class) (uint16, error) {
	for _, t := range class.ClassTraits.Slots {
		if t.Name == "protocolId" {
			if t.Source.Kind != bytecode.TraitsInfoConst {
				return 0, ErrExtractProtocolIDNotConst
			}
			if t.Source.VKind != bytecode.SlotKindInt {
				return 0, ErrExtractProtocolIDNotInt
			}
			id := b.abcFile.Source.ConstantPool.Integers[t.Source.VIndex]
			return uint16(id), nil
		}
	}
	return 0, ErrExtractNoProtocolID
}

func (b *builder) extractMessageFields(class as3.Class) (f []Field, err error) {
	createField := func(name string, typeId uint32) Field {
		t := b.abcFile.Source.ConstantPool.MultinameString(typeId)
		var isVector bool
		if strings.HasPrefix(t, "Vector<") {
			typename := b.abcFile.Source.ConstantPool.Multinames[typeId]
			param := b.abcFile.Source.ConstantPool.MultinameString(typename.Params[0])
			t = param
			isVector = true
		} else if t == "ByteArray" {
			isVector = true
			t = "uint"
		}
		return Field{Name: name, Type: t, IsVector: isVector}
	}

	for _, slot := range class.InstanceTraits.Slots {
		name := b.abcFile.Source.ConstantPool.Multinames[slot.Source.Name]
		if !isPublicNamespace(b.abcFile, name.Namespace) {
			continue
		}
		field := createField(slot.Name, slot.Source.Typename)
		f = append(f, field)
	}

	// NetworkDataContainerMessage uses a pair of setter/getter to store content
	// It seems to be useless and the only packet that does so we need to
	// also check for pairs of getter/setter
	type getSetter struct {
		getter     bool
		getterType uint32
		setter     bool
	}
	getSetters := map[string]*getSetter{}

	for _, m := range class.InstanceTraits.Methods {
		isGetter := m.Source.Kind == bytecode.TraitsInfoGetter
		isSetter := m.Source.Kind == bytecode.TraitsInfoSetter
		name := b.abcFile.Source.ConstantPool.Multinames[m.Source.Name]
		if !(isGetter || isSetter) || !isPublicNamespace(b.abcFile, name.Namespace) {
			continue
		}
		v, ok := getSetters[m.Name]
		if !ok {
			v = &getSetter{}
			getSetters[m.Name] = v
		}
		v.getter = v.getter || isGetter
		v.setter = v.setter || isSetter
		if isGetter {
			info := b.abcFile.Source.Methods[m.Source.Method]
			v.getterType = info.ReturnType
		}
	}

	for name, gs := range getSetters {
		if !(gs.getter && gs.setter) {
			continue
		}
		field := createField(name, gs.getterType)
		f = append(f, field)
	}
	return
}

func handleSimpleProp(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	get := instrs[0]
	call := instrs[1]
	getMultiname := b.abcFile.Source.ConstantPool.Multinames[get.Operands[0]]
	callMultiname := b.abcFile.Source.ConstantPool.Multinames[call.Operands[0]]
	if !isPublicQName(b.abcFile, getMultiname) {
		return nil, nil
	}

	prop := b.abcFile.Source.ConstantPool.Strings[getMultiname.Name]
	writeMethod := b.abcFile.Source.ConstantPool.Strings[callMultiname.Name]

	if !strings.HasPrefix(writeMethod, "write") {
		return nil, nil
	}

	field, ok := fields[prop]
	if !ok {
		return nil, fmt.Errorf("%v.%v.%v field not found", class.Namespace, class.Name, prop)
	}

	field.WriteMethod = writeMethod
	return field, nil
}

func handleVecPropLength(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	get := instrs[0]
	getLen := instrs[1]
	call := instrs[2]

	getMultiname := b.abcFile.Source.ConstantPool.Multinames[get.Operands[0]]
	getLenMultiname := b.abcFile.Source.ConstantPool.Multinames[getLen.Operands[0]]
	callMultiname := b.abcFile.Source.ConstantPool.Multinames[call.Operands[0]]
	if !isPublicQName(b.abcFile, getMultiname) || !isPublicQName(b.abcFile, getLenMultiname) {
		return nil, nil
	}

	if b.abcFile.Source.ConstantPool.Strings[getLenMultiname.Name] != "length" {
		return nil, nil
	}
	prop := b.abcFile.Source.ConstantPool.Strings[getMultiname.Name]

	field, ok := fields[prop]
	if !ok || !field.IsVector {
		return nil, fmt.Errorf("%v.%v: write length on non-vector %v", class.Namespace, class.Name, prop)
	}
	writeMethod := b.abcFile.Source.ConstantPool.Strings[callMultiname.Name]

	if !strings.HasPrefix(writeMethod, "write") {
		return nil, nil
	}

	field.IsDynamicLength = true
	field.WriteLengthMethod = writeMethod
	return field, nil
}

func handleTypeManagerProp(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	get := instrs[0]
	getType := instrs[1]
	call := instrs[2]

	getMultiname := b.abcFile.Source.ConstantPool.Multinames[get.Operands[0]]
	getTypeMultiname := b.abcFile.Source.ConstantPool.Multinames[getType.Operands[0]]
	callMultiname := b.abcFile.Source.ConstantPool.Multinames[call.Operands[0]]

	if !isPublicQName(b.abcFile, getMultiname) || !isPublicQName(b.abcFile, getTypeMultiname) {
		return nil, nil
	}

	if b.abcFile.Source.ConstantPool.Strings[getTypeMultiname.Name] != "getTypeId" {
		return nil, nil
	}

	prop := b.abcFile.Source.ConstantPool.Strings[getMultiname.Name]
	field, ok := fields[prop]
	if !ok {
		return nil, fmt.Errorf("%v.%v: getTypeId on %v field", class.Namespace, class.Name, prop)
	}

	writeMethod := b.abcFile.Source.ConstantPool.Strings[callMultiname.Name]
	if writeMethod != "writeShort" {
		return nil, fmt.Errorf("%v.%v: invalid %v for getTypeId", class.Namespace, class.Name, writeMethod)
	}

	field.UseTypeManager = true
	return field, nil
}

func handleVecScalarProp(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	get := instrs[0]
	getIndex := instrs[2]
	getMultiname := b.abcFile.Source.ConstantPool.Multinames[get.Operands[0]]
	getIndexMultiname := b.abcFile.Source.ConstantPool.Multinames[getIndex.Operands[0]]
	if !isPublicQName(b.abcFile, getMultiname) || getIndexMultiname.Kind != bytecode.MultinameKindMultinameL {
		return nil, nil
	}

	call := instrs[3]
	callMultiname := b.abcFile.Source.ConstantPool.Multinames[call.Operands[0]]
	if callMultiname.Kind != bytecode.MultinameKindQName {
		return nil, nil
	}

	writeMethod := b.abcFile.Source.ConstantPool.Strings[callMultiname.Name]
	if !strings.HasPrefix(writeMethod, "write") {
		return nil, fmt.Errorf("%v.%v: %v method for vector of scalar types", class.Namespace, class.Name, writeMethod)
	}

	prop := b.abcFile.Source.ConstantPool.Strings[getMultiname.Name]
	field, ok := fields[prop]
	if !ok || !field.IsVector {
		return nil, fmt.Errorf("%v.%v: vector of scalar write on %v field", class.Namespace, class.Name, prop)
	}
	field.WriteMethod = writeMethod
	return field, nil
}

func handleVecTypeManagerProp(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	get := instrs[0]
	lex := instrs[3]
	call := instrs[5]
	getMultiname := b.abcFile.Source.ConstantPool.Multinames[get.Operands[0]]
	lexMultiname := b.abcFile.Source.ConstantPool.Multinames[lex.Operands[0]]
	callMultiname := b.abcFile.Source.ConstantPool.Multinames[call.Operands[0]]

	if !isPublicQName(b.abcFile, getMultiname) {
		return nil, nil
	}

	lexNs := b.abcFile.Source.ConstantPool.Namespaces[lexMultiname.Namespace]
	lexNsName := b.abcFile.Source.ConstantPool.Strings[lexNs.Name]
	if !strings.HasPrefix(lexNsName, "com.ankamagames.dofus.network.types") {
		return nil, nil
	}

	callName := b.abcFile.Source.ConstantPool.Strings[callMultiname.Name]
	if callName != "getTypeId" {
		return nil, nil
	}

	prop := b.abcFile.Source.ConstantPool.Strings[getMultiname.Name]
	f, ok := fields[prop]
	if !ok || !f.IsVector {
		return nil, fmt.Errorf("%v.%v: %v field is not a vector", class.Namespace, class.Name, prop)
	}

	f.UseTypeManager = true
	return f, nil
}

func handleVecPropDynamicLen(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	push := instrs[5]
	len := push.Operands[0]
	if last == nil || !last.IsVector || last.IsDynamicLength {
		return nil, errors.New("vector length found but no dynamic vector")
	}
	last.Length = len
	return last, nil
}

func handleGetProperty(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	get := instrs[0]
	multi := b.abcFile.Source.ConstantPool.Multinames[get.Operands[0]]
	if !isPublicQName(b.abcFile, multi) {
		return nil, nil
	}
	name := b.abcFile.Source.ConstantPool.Strings[multi.Name]
	field, ok := fields[name]
	if !ok {
		return nil, nil
	}
	return field, nil
}

func handleBBWProp(b *builder, class as3.Class, fields map[string]*Field, instrs []bytecode.Instr, last *Field) (*Field, error) {
	lex := instrs[0]
	lexMultiname := b.abcFile.Source.ConstantPool.Multinames[lex.Operands[0]]
	lexName := b.abcFile.Source.ConstantPool.Strings[lexMultiname.Name]
	if lexName != "BooleanByteWrapper" {
		return nil, nil
	}

	push := instrs[2]
	position := uint(push.Operands[0])

	getProp := instrs[4]
	propMultiname := b.abcFile.Source.ConstantPool.Multinames[getProp.Operands[0]]
	prop := b.abcFile.Source.ConstantPool.Strings[propMultiname.Name]

	field, ok := fields[prop]
	if !ok || field.Type != "Boolean" {
		return nil, fmt.Errorf("%v.%v: %v usage of BooleanByteWrapper on non boolean field", class.Namespace, class.Name, prop)
	}

	field.UseBBW = true
	field.BBWPosition = position
	return field, nil
}

func (b *builder) extractSerializeMethods(class as3.Class, m as3.Method, fields map[string]*Field) error {
	checkPattern := func(instrs []bytecode.Instr, pattern []string) bool {
		if len(pattern) > len(instrs) {
			return false
		}
		for i, str := range pattern {
			if !strings.HasPrefix(instrs[i].Model.Name, str) {
				return false
			}
		}
		return true
	}

	type pattern struct {
		Fn      func(*builder, as3.Class, map[string]*Field, []bytecode.Instr, *Field) (*Field, error)
		Pattern []string
	}

	// These must be sorted by pattern length to be sure to not miss any pattern
	patterns := []pattern{
		{handleVecPropDynamicLen, []string{"getlocal", "increment", "convert", "setlocal", "getlocal", "pushbyte", "iflt"}},
		{handleVecTypeManagerProp, []string{"getproperty", "getlocal", "getproperty", "getlex", "astypelate", "callproperty"}},
		{handleBBWProp, []string{"getlex", "getlocal", "pushbyte", "getlocal", "getproperty", "callproperty"}},
		{handleVecScalarProp, []string{"getproperty", "getlocal", "getproperty", "callpropvoid"}},
		{handleVecPropLength, []string{"getproperty", "getproperty", "callpropvoid"}},
		{handleSimpleProp, []string{"getproperty", "callpropvoid"}},
		{handleTypeManagerProp, []string{"getproperty", "callproperty", "callpropvoid"}},
		{handleGetProperty, []string{"getproperty"}},
	}

	instrs := m.BodyInfo.Instructions
	instrLen := len(m.BodyInfo.Instructions)
	var last *Field
	for i := 0; i < instrLen; {
		var f *Field
		var err error
		for _, p := range patterns {
			if checkPattern(instrs[i:], p.Pattern) {
				f, err = p.Fn(b, class, fields, instrs[i:], last)
				if err != nil {
					return err
				}
				i += len(p.Pattern)
			}
		}
		if f == nil {
			i++
		} else {
			last = f
		}
	}
	return nil
}

func (b *builder) ExtractVersion() (Version, error) {
	findBuildInfos := func() *as3.Class {
		for _, c := range b.abcFile.Classes {
			if c.Namespace == "com.ankamagames.dofus" && c.Name == "BuildInfos" {
				return &c
			}
		}
		return nil
	}

	extractValue := func(i bytecode.Instr) (uint, error) {
		if i.Model.Name == "pushbyte" {
			return uint(i.Operands[0]), nil
		} else if i.Model.Name == "pushint" {
			v := b.abcFile.Source.ConstantPool.Integers[i.Operands[0]]
			return uint(v), nil
		}
		return 0, fmt.Errorf("%v instruction detected when extracting version", i.Model.Name)
	}

	extractFromString := func(x string) (uint, error) {
		n, err := strconv.Atoi(x)
		if err != nil {
			return 0, err
		}
		return uint(n), nil
	}

	buildInfos := findBuildInfos()
	if buildInfos == nil {
		return Version{}, ErrExtractNoBuildInfos
	}

	m := b.abcFile.Methods[buildInfos.ClassInfo.CInit]
	if err := m.BodyInfo.Disassemble(); err != nil {
		return Version{}, fmt.Errorf("could not disassemble BuildInfos: %v", err)
	}

	instrs := m.BodyInfo.Instructions

	// New versions of Dofus uses a new way to format the Version.
	// public static var VERSION:Version = new Version("2.42.0",BuildTypeEnum.RELEASE,1027565,0);

	// Version 2.46 adds Debug informations
	var major, minor, release, revision, patch uint
	var err error

	fmt.Println(len(instrs))
	fmt.Println(instrs)
	if instrs[2].Model.Name == "debug" {
		majMinRelInstr := instrs[5]
		revInstr := instrs[8]
		patchInstr := instrs[9]

		strIdx := majMinRelInstr.Operands[0]
		// string of format "MAJOR.MINOR.RELEASE"
		majMinRel := strings.Split(b.abcFile.Source.ConstantPool.Strings[strIdx], ".")
		major, err = extractFromString(majMinRel[0])
		if err != nil {
			return Version{}, err
		}
		minor, err = extractFromString(majMinRel[1])
		if err != nil {
			return Version{}, err
		}
		release, err = extractFromString(majMinRel[2])
		if err != nil {
			return Version{}, err
		}
		revision, err = extractValue(revInstr)
		if err != nil {
			return Version{}, err
		}
		patch, err = extractValue(patchInstr)
		if err != nil {
			return Version{}, err
		}
	} else if instrs[4].Model.Name == "pushstring" {
		majMinRelInstr := instrs[4]
		revInstr := instrs[7]
		patchInstr := instrs[8]

		strIdx := majMinRelInstr.Operands[0]
		// string of format "MAJOR.MINOR.RELEASE"
		majMinRel := strings.Split(b.abcFile.Source.ConstantPool.Strings[strIdx], ".")
		major, err = extractFromString(majMinRel[0])
		if err != nil {
			return Version{}, err
		}
		minor, err = extractFromString(majMinRel[1])
		if err != nil {
			return Version{}, err
		}
		release, err = extractFromString(majMinRel[2])
		if err != nil {
			return Version{}, err
		}
		revision, err = extractValue(revInstr)
		if err != nil {
			return Version{}, err
		}
		patch, err = extractValue(patchInstr)
		if err != nil {
			return Version{}, err
		}
	} else {
		majInstr := instrs[4]
		minInstr := instrs[5]
		relInstr := instrs[6]
		revInstr := instrs[14]
		patchInstr := instrs[17]

		major, err = extractValue(majInstr)
		if err != nil {
			return Version{}, err
		}
		minor, err = extractValue(minInstr)
		if err != nil {
			return Version{}, err
		}
		release, err = extractValue(relInstr)
		if err != nil {
			return Version{}, err
		}
		revision, err = extractValue(revInstr)
		if err != nil {
			return Version{}, err
		}
		patch, err = extractValue(patchInstr)
		if err != nil {
			return Version{}, err
		}
	}

	return Version{major, minor, release, revision, patch}, nil
}
