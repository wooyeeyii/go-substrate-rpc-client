package registry

import (
	"errors"
	"fmt"
	"strings"

	"github.com/centrifuge/go-substrate-rpc-client/v4/scale"
	"github.com/centrifuge/go-substrate-rpc-client/v4/types"
)

// Factory is the interface responsible for generating the according registries from the metadata.
type Factory interface {
	CreateCallRegistry(meta *types.Metadata) (CallRegistry, error)
	CreateErrorRegistry(meta *types.Metadata) (ErrorRegistry, error)
	CreateEventRegistry(meta *types.Metadata) (EventRegistry, error)
}

// CallRegistry maps a call name to its Type.
type CallRegistry map[string]*Type

// ErrorRegistry maps an error name to its Type.
type ErrorRegistry map[string]*Type

// EventRegistry maps an event ID to its Type.
type EventRegistry map[types.EventID]*Type

type factory struct {
	fieldStorage          map[int64]FieldDecoder
	recursiveFieldStorage map[int64]*RecursiveDecoder
}

// NewFactory creates a new Factory.
func NewFactory() Factory {
	return &factory{}
}

// CreateErrorRegistry creates the registry that contains the types for errors.
// nolint:dupl
func (f *factory) CreateErrorRegistry(meta *types.Metadata) (ErrorRegistry, error) {
	f.initStorages()

	errorRegistry := make(map[string]*Type)

	for _, mod := range meta.AsMetadataV14.Pallets {
		if !mod.HasErrors {
			continue
		}

		errorsType, ok := meta.AsMetadataV14.EfficientLookup[mod.Errors.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("errors type %d not found for module '%s'", mod.Errors.Type.Int64(), mod.Name)
		}

		if !errorsType.Def.IsVariant {
			return nil, fmt.Errorf("errors type %d for module '%s' is not a variant", mod.Errors.Type.Int64(), mod.Name)
		}

		for _, errorVariant := range errorsType.Def.Variant.Variants {
			errorName := fmt.Sprintf("%s.%s", mod.Name, errorVariant.Name)

			errorFields, err := f.getTypeFields(meta, errorVariant.Fields)

			if err != nil {
				return nil, fmt.Errorf("couldn't get fields for error '%s': %w", errorName, err)
			}

			errorRegistry[errorName] = &Type{
				Name:   errorName,
				Fields: errorFields,
			}
		}
	}

	if err := f.resolveRecursiveDecoders(); err != nil {
		return nil, err
	}

	return errorRegistry, nil
}

// CreateCallRegistry creates the registry that contains the types for calls.
// nolint:dupl
func (f *factory) CreateCallRegistry(meta *types.Metadata) (CallRegistry, error) {
	f.initStorages()

	callRegistry := make(map[string]*Type)

	for _, mod := range meta.AsMetadataV14.Pallets {
		if !mod.HasCalls {
			continue
		}

		callsType, ok := meta.AsMetadataV14.EfficientLookup[mod.Calls.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("calls type %d not found for module '%s'", mod.Calls.Type.Int64(), mod.Name)
		}

		if !callsType.Def.IsVariant {
			return nil, fmt.Errorf("calls type %d for module '%s' is not a variant", mod.Calls.Type.Int64(), mod.Name)
		}

		for _, callVariant := range callsType.Def.Variant.Variants {
			callName := fmt.Sprintf("%s.%s", mod.Name, callVariant.Name)

			callFields, err := f.getTypeFields(meta, callVariant.Fields)

			if err != nil {
				return nil, fmt.Errorf("couldn't get fields for call '%s': %w", callName, err)
			}

			callRegistry[callName] = &Type{
				Name:   callName,
				Fields: callFields,
			}
		}
	}

	if err := f.resolveRecursiveDecoders(); err != nil {
		return nil, err
	}

	return callRegistry, nil
}

// CreateEventRegistry creates the registry that contains the types for events.
func (f *factory) CreateEventRegistry(meta *types.Metadata) (EventRegistry, error) {
	f.initStorages()

	eventRegistry := make(map[types.EventID]*Type)

	for _, mod := range meta.AsMetadataV14.Pallets {
		if !mod.HasEvents {
			continue
		}

		eventsType, ok := meta.AsMetadataV14.EfficientLookup[mod.Events.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("events type %d not found for module '%s'", mod.Events.Type.Int64(), mod.Name)
		}

		if !eventsType.Def.IsVariant {
			return nil, fmt.Errorf("events type %d for module '%s' is not a variant", mod.Events.Type.Int64(), mod.Name)
		}

		for _, eventVariant := range eventsType.Def.Variant.Variants {
			eventID := types.EventID{byte(mod.Index), byte(eventVariant.Index)}

			eventName := fmt.Sprintf("%s.%s", mod.Name, eventVariant.Name)

			eventFields, err := f.getTypeFields(meta, eventVariant.Fields)

			if err != nil {
				return nil, fmt.Errorf("couldn't get fields for event '%s': %w", eventName, err)
			}

			eventRegistry[eventID] = &Type{
				Name:   eventName,
				Fields: eventFields,
			}
		}
	}

	if err := f.resolveRecursiveDecoders(); err != nil {
		return nil, err
	}

	return eventRegistry, nil
}

// initStorages initializes the storages used when creating registries.
func (f *factory) initStorages() {
	f.fieldStorage = make(map[int64]FieldDecoder)
	f.recursiveFieldStorage = make(map[int64]*RecursiveDecoder)
}

// resolveRecursiveDecoders resolves all recursive decoders with their according FieldDecoder.
// nolint:lll
func (f *factory) resolveRecursiveDecoders() error {
	for recursiveFieldLookupIndex, recursiveFieldDecoder := range f.recursiveFieldStorage {
		fieldDecoder, ok := f.fieldStorage[recursiveFieldLookupIndex]

		if !ok {
			return fmt.Errorf("couldn't get field decoder for recursive field with lookup index %d", recursiveFieldLookupIndex)
		}

		if _, ok := fieldDecoder.(*RecursiveDecoder); ok {
			return fmt.Errorf("recursive field decoder with lookup index %d cannot be resolved with a non-recursive field decoder", recursiveFieldLookupIndex)
		}

		recursiveFieldDecoder.FieldDecoder = fieldDecoder
	}

	return nil
}

// getTypeFields parses and returns all Field(s) for a type.
func (f *factory) getTypeFields(meta *types.Metadata, fields []types.Si1Field) ([]*Field, error) {
	var typeFields []*Field

	for _, field := range fields {
		fieldType, ok := meta.AsMetadataV14.EfficientLookup[field.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("type not found for field '%s'", field.Name)
		}

		fieldName := getFieldName(field, fieldType)

		if storedFieldDecoder, ok := f.getStoredFieldDecoder(field.Type.Int64()); ok {
			typeFields = append(typeFields, &Field{
				Name:         fieldName,
				FieldDecoder: storedFieldDecoder,
				LookupIndex:  field.Type.Int64(),
			})
			continue
		}

		fieldTypeDef := fieldType.Def

		fieldDecoder, err := f.getFieldDecoder(meta, fieldName, fieldTypeDef)

		if err != nil {
			return nil, fmt.Errorf("couldn't get field decoder for '%s': %w", fieldName, err)
		}

		f.fieldStorage[field.Type.Int64()] = fieldDecoder

		typeFields = append(typeFields, &Field{
			Name:         fieldName,
			FieldDecoder: fieldDecoder,
			LookupIndex:  field.Type.Int64(),
		})
	}

	return typeFields, nil
}

// getFieldDecoder returns the FieldDecoder based on the provided type definition.
// nolint:funlen
func (f *factory) getFieldDecoder(meta *types.Metadata, fieldName string, typeDef types.Si1TypeDef) (FieldDecoder, error) {
	switch {
	case typeDef.IsCompact:
		compactFieldType, ok := meta.AsMetadataV14.EfficientLookup[typeDef.Compact.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("type not found for compact field with name '%s'", fieldName)
		}

		return f.getCompactFieldDecoder(meta, fieldName, compactFieldType.Def)
	case typeDef.IsComposite:
		compositeDecoder := &CompositeDecoder{
			FieldName: fieldName,
		}

		fields, err := f.getTypeFields(meta, typeDef.Composite.Fields)

		if err != nil {
			return nil, fmt.Errorf("couldn't get fields for composite type with name '%s': %w", fieldName, err)
		}

		compositeDecoder.Fields = fields

		return compositeDecoder, nil
	case typeDef.IsVariant:
		return f.getVariantFieldDecoder(meta, typeDef)
	case typeDef.IsPrimitive:
		return getPrimitiveDecoder(typeDef.Primitive.Si0TypeDefPrimitive)
	case typeDef.IsArray:
		arrayFieldType, ok := meta.AsMetadataV14.EfficientLookup[typeDef.Array.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("type not found for array field with name '%s'", fieldName)
		}

		return f.getArrayFieldDecoder(uint(typeDef.Array.Len), meta, fieldName, arrayFieldType.Def)
	case typeDef.IsSequence:
		vectorFieldType, ok := meta.AsMetadataV14.EfficientLookup[typeDef.Sequence.Type.Int64()]

		if !ok {
			return nil, fmt.Errorf("type not found for vector field with name '%s'", fieldName)
		}

		return f.getSliceFieldDecoder(meta, fieldName, vectorFieldType.Def)
	case typeDef.IsTuple:
		if typeDef.Tuple == nil {
			return &NoopDecoder{}, nil
		}

		return f.getTupleFieldDecoder(meta, fieldName, typeDef.Tuple)
	case typeDef.IsBitSequence:
		bitStoreType, ok := meta.AsMetadataV14.EfficientLookup[typeDef.BitSequence.BitStoreType.Int64()]

		if !ok {
			return nil, errors.New("bit store type not found")
		}

		bitStoreFieldDecoder, err := f.getFieldDecoder(meta, bitStoreKey, bitStoreType.Def)

		if err != nil {
			return nil, fmt.Errorf("couldn't get bit store field type: %w", err)
		}

		bitOrderType, ok := meta.AsMetadataV14.EfficientLookup[typeDef.BitSequence.BitOrderType.Int64()]

		if !ok {
			return nil, errors.New("bit order type not found")
		}

		bitOrderFieldDecoder, err := f.getFieldDecoder(meta, bitOrderKey, bitOrderType.Def)

		if err != nil {
			return nil, fmt.Errorf("couldn't get bit order field decoder: %w", err)
		}

		return &BitSequenceDecoder{
			BitStoreFieldDecoder: bitStoreFieldDecoder,
			BitOrderFieldDecoder: bitOrderFieldDecoder,
		}, nil
	default:
		return nil, errors.New("unsupported field type definition")
	}
}

const (
	variantItemFieldNameFormat = "variant_item_%d"
)

// getVariantFieldDecoder parses a variant type definition and returns a VariantDecoder.
func (f *factory) getVariantFieldDecoder(meta *types.Metadata, typeDef types.Si1TypeDef) (FieldDecoder, error) {
	variantDecoder := &VariantDecoder{}

	fieldDecoderMap := make(map[byte]FieldDecoder)

	for i, variant := range typeDef.Variant.Variants {
		if len(variant.Fields) == 0 {
			fieldDecoderMap[byte(variant.Index)] = &NoopDecoder{}
			continue
		}

		variantFieldName := fmt.Sprintf(variantItemFieldNameFormat, i)

		compositeDecoder := &CompositeDecoder{
			FieldName: variantFieldName,
		}

		fields, err := f.getTypeFields(meta, variant.Fields)

		if err != nil {
			return nil, fmt.Errorf("couldn't get type fields for variant '%d': %w", variant.Index, err)
		}

		compositeDecoder.Fields = fields

		fieldDecoderMap[byte(variant.Index)] = compositeDecoder
	}

	variantDecoder.FieldDecoderMap = fieldDecoderMap

	return variantDecoder, nil
}

const (
	tupleItemFieldNameFormat = "tuple_item_%d"
)

// getCompactFieldDecoder parses a compact type definition and returns the according field decoder.
// nolint:funlen,lll
func (f *factory) getCompactFieldDecoder(meta *types.Metadata, fieldName string, typeDef types.Si1TypeDef) (FieldDecoder, error) {
	switch {
	case typeDef.IsPrimitive:
		return &ValueDecoder[types.UCompact]{}, nil
	case typeDef.IsTuple:
		if typeDef.Tuple == nil {
			return &ValueDecoder[any]{}, nil
		}

		compositeDecoder := &CompositeDecoder{
			FieldName: fieldName,
		}

		for i, item := range typeDef.Tuple {
			itemTypeDef, ok := meta.AsMetadataV14.EfficientLookup[item.Int64()]

			if !ok {
				return nil, fmt.Errorf("type definition for tuple item %d not found", item.Int64())
			}

			fieldName := fmt.Sprintf(tupleItemFieldNameFormat, i)

			itemFieldDecoder, err := f.getCompactFieldDecoder(meta, fieldName, itemTypeDef.Def)

			if err != nil {
				return nil, fmt.Errorf("couldn't get tuple field decoder: %w", err)
			}

			compositeDecoder.Fields = append(compositeDecoder.Fields, &Field{
				Name:         fieldName,
				FieldDecoder: itemFieldDecoder,
				LookupIndex:  item.Int64(),
			})
		}

		return compositeDecoder, nil
	case typeDef.IsComposite:
		compactCompositeFields := typeDef.Composite.Fields

		compositeDecoder := &CompositeDecoder{
			FieldName: fieldName,
		}

		for _, compactCompositeField := range compactCompositeFields {
			compactCompositeFieldType, ok := meta.AsMetadataV14.EfficientLookup[compactCompositeField.Type.Int64()]

			if !ok {
				return nil, errors.New("compact composite field type not found")
			}

			compactFieldName := getFieldName(compactCompositeField, compactCompositeFieldType)

			compactCompositeDecoder, err := f.getCompactFieldDecoder(meta, compactFieldName, compactCompositeFieldType.Def)

			if err != nil {
				return nil, fmt.Errorf("couldn't decode compact composite type: %w", err)
			}

			compositeDecoder.Fields = append(compositeDecoder.Fields, &Field{
				Name:         compactFieldName,
				FieldDecoder: compactCompositeDecoder,
				LookupIndex:  compactCompositeField.Type.Int64(),
			})
		}

		return compositeDecoder, nil
	default:
		return nil, errors.New("unsupported compact field type")
	}
}

// getArrayFieldDecoder parses an array type definition and returns an ArrayDecoder.
// nolint:lll
func (f *factory) getArrayFieldDecoder(arrayLen uint, meta *types.Metadata, fieldName string, typeDef types.Si1TypeDef) (FieldDecoder, error) {
	itemFieldDecoder, err := f.getFieldDecoder(meta, fieldName, typeDef)

	if err != nil {
		return nil, fmt.Errorf("couldn't get array item field decoder: %w", err)
	}

	return &ArrayDecoder{Length: arrayLen, ItemDecoder: itemFieldDecoder}, nil
}

// getSliceFieldDecoder parses a slice type definition and returns an SliceDecoder.
// nolint:lll
func (f *factory) getSliceFieldDecoder(meta *types.Metadata, fieldName string, typeDef types.Si1TypeDef) (FieldDecoder, error) {
	itemFieldDecoder, err := f.getFieldDecoder(meta, fieldName, typeDef)

	if err != nil {
		return nil, fmt.Errorf("couldn't get slice item field decoder: %w", err)
	}

	return &SliceDecoder{itemFieldDecoder}, nil
}

// getTupleFieldDecoder parses a tuple type definition and returns a CompositeDecoder.
func (f *factory) getTupleFieldDecoder(meta *types.Metadata, fieldName string, tuple types.Si1TypeDefTuple) (FieldDecoder, error) {
	compositeDecoder := &CompositeDecoder{
		FieldName: fieldName,
	}

	for i, item := range tuple {
		itemTypeDef, ok := meta.AsMetadataV14.EfficientLookup[item.Int64()]

		if !ok {
			return nil, fmt.Errorf("type definition for tuple item %d not found", i)
		}

		tupleFieldName := fmt.Sprintf(tupleItemFieldNameFormat, i)

		itemFieldDecoder, err := f.getFieldDecoder(meta, tupleFieldName, itemTypeDef.Def)

		if err != nil {
			return nil, fmt.Errorf("couldn't get field decoder for tuple item %d: %w", i, err)
		}

		compositeDecoder.Fields = append(compositeDecoder.Fields, &Field{
			Name:         tupleFieldName,
			FieldDecoder: itemFieldDecoder,
			LookupIndex:  item.Int64(),
		})
	}

	return compositeDecoder, nil
}

// getPrimitiveDecoder parses a primitive type definition and returns a ValueDecoder.
func getPrimitiveDecoder(primitiveTypeDef types.Si0TypeDefPrimitive) (FieldDecoder, error) {
	switch primitiveTypeDef {
	case types.IsBool:
		return &ValueDecoder[bool]{}, nil
	case types.IsChar:
		return &ValueDecoder[byte]{}, nil
	case types.IsStr:
		return &ValueDecoder[string]{}, nil
	case types.IsU8:
		return &ValueDecoder[types.U8]{}, nil
	case types.IsU16:
		return &ValueDecoder[types.U16]{}, nil
	case types.IsU32:
		return &ValueDecoder[types.U32]{}, nil
	case types.IsU64:
		return &ValueDecoder[types.U64]{}, nil
	case types.IsU128:
		return &ValueDecoder[types.U128]{}, nil
	case types.IsU256:
		return &ValueDecoder[types.U256]{}, nil
	case types.IsI8:
		return &ValueDecoder[types.I8]{}, nil
	case types.IsI16:
		return &ValueDecoder[types.I16]{}, nil
	case types.IsI32:
		return &ValueDecoder[types.I32]{}, nil
	case types.IsI64:
		return &ValueDecoder[types.I64]{}, nil
	case types.IsI128:
		return &ValueDecoder[types.I128]{}, nil
	case types.IsI256:
		return &ValueDecoder[types.I256]{}, nil
	default:
		return nil, fmt.Errorf("unsupported primitive type %v", primitiveTypeDef)
	}
}

// getStoredFieldDecoder will attempt to return a FieldDecoder from storage, and perform an extra check for recursive decoders.
func (f *factory) getStoredFieldDecoder(fieldLookupIndex int64) (FieldDecoder, bool) {
	if ft, ok := f.fieldStorage[fieldLookupIndex]; ok {
		if rt, ok := ft.(*RecursiveDecoder); ok {
			f.recursiveFieldStorage[fieldLookupIndex] = rt
		}

		return ft, ok
	}

	// Ensure that a recursive type such as Xcm::TransferReserveAsset does not cause an infinite loop
	// by adding the RecursiveDecoder the first time the field is encountered.
	f.fieldStorage[fieldLookupIndex] = &RecursiveDecoder{}

	return nil, false
}

const (
	fieldPathSeparator = "_"
	lookupIndexFormat  = "lookup_index_%d"
)

func getFieldPath(fieldType *types.Si1Type) string {
	var nameParts []string

	for _, pathEntry := range fieldType.Path {
		nameParts = append(nameParts, string(pathEntry))
	}

	return strings.Join(nameParts, fieldPathSeparator)
}

func getFieldName(field types.Si1Field, fieldType *types.Si1Type) string {
	if fieldPath := getFieldPath(fieldType); fieldPath != "" {
		return fieldPath
	}

	switch {
	case field.HasName:
		return string(field.Name)
	case field.HasTypeName:
		return string(field.TypeName)
	default:
		return fmt.Sprintf(lookupIndexFormat, field.Type.Int64())
	}
}

// Type represents a parsed metadata type.
type Type struct {
	Name   string
	Fields []*Field
}

func (t *Type) Decode(decoder *scale.Decoder) (map[string]any, error) {
	fieldMap := make(map[string]any)

	for _, field := range t.Fields {
		value, err := field.FieldDecoder.Decode(decoder)

		if err != nil {
			return nil, err
		}

		fieldMap[field.Name] = value
	}

	return fieldMap, nil
}

// Field represents one field of a Type.
type Field struct {
	Name         string
	FieldDecoder FieldDecoder
	LookupIndex  int64
}

// FieldDecoder is the interface implemented by all the different types that are available.
type FieldDecoder interface {
	Decode(decoder *scale.Decoder) (any, error)
}

// NoopDecoder is a FieldDecoder that does not decode anything. It comes in handy for nil tuples or variants
// with no inner types.
type NoopDecoder struct{}

func (n *NoopDecoder) Decode(_ *scale.Decoder) (any, error) {
	return nil, nil
}

// VariantDecoder holds a FieldDecoder for each variant/enum.
type VariantDecoder struct {
	FieldDecoderMap map[byte]FieldDecoder
}

func (v *VariantDecoder) Decode(decoder *scale.Decoder) (any, error) {
	variantByte, err := decoder.ReadOneByte()

	if err != nil {
		return nil, fmt.Errorf("couldn't read variant byte: %w", err)
	}

	variantDecoder, ok := v.FieldDecoderMap[variantByte]

	if !ok {
		return nil, fmt.Errorf("variant decoder for variant %d not found", variantByte)
	}

	if _, ok := variantDecoder.(*NoopDecoder); ok {
		return variantByte, nil
	}

	return variantDecoder.Decode(decoder)
}

// ArrayDecoder holds information about the length of the array and the FieldDecoder used for its items.
type ArrayDecoder struct {
	Length      uint
	ItemDecoder FieldDecoder
}

func (a *ArrayDecoder) Decode(decoder *scale.Decoder) (any, error) {
	if a.ItemDecoder == nil {
		return nil, errors.New("array item decoder not found")
	}

	slice := make([]any, 0, a.Length)

	for i := uint(0); i < a.Length; i++ {
		item, err := a.ItemDecoder.Decode(decoder)

		if err != nil {
			return nil, err
		}

		slice = append(slice, item)
	}

	return slice, nil
}

// SliceDecoder holds a FieldDecoder for the items of a vector/slice.
type SliceDecoder struct {
	ItemDecoder FieldDecoder
}

func (s *SliceDecoder) Decode(decoder *scale.Decoder) (any, error) {
	if s.ItemDecoder == nil {
		return nil, errors.New("slice item decoder not found")
	}

	sliceLen, err := decoder.DecodeUintCompact()

	if err != nil {
		return nil, fmt.Errorf("couldn't decode slice length: %w", err)
	}

	slice := make([]any, 0, sliceLen.Uint64())

	for i := uint64(0); i < sliceLen.Uint64(); i++ {
		item, err := s.ItemDecoder.Decode(decoder)

		if err != nil {
			return nil, err
		}

		slice = append(slice, item)
	}

	return slice, nil
}

// CompositeDecoder holds all the information required to decoder a struct/composite.
type CompositeDecoder struct {
	FieldName string
	Fields    []*Field
}

func (e *CompositeDecoder) Decode(decoder *scale.Decoder) (any, error) {
	fieldMap := make(map[string]any)

	for _, field := range e.Fields {
		value, err := field.FieldDecoder.Decode(decoder)

		if err != nil {
			return nil, err
		}

		fieldMap[field.Name] = value
	}

	return fieldMap, nil
}

// ValueDecoder decodes a primitive type.
type ValueDecoder[T any] struct{}

func (v *ValueDecoder[T]) Decode(decoder *scale.Decoder) (any, error) {
	var t T

	if err := decoder.Decode(&t); err != nil {
		return nil, err
	}

	return t, nil
}

// RecursiveDecoder is a wrapper for a FieldDecoder that is recursive.
type RecursiveDecoder struct {
	FieldDecoder FieldDecoder
}

func (r *RecursiveDecoder) Decode(decoder *scale.Decoder) (any, error) {
	if r.FieldDecoder == nil {
		return nil, errors.New("recursive field decoder not found")
	}

	return r.FieldDecoder.Decode(decoder)
}

// BitSequenceDecoder holds the decoders for the bit store and the bit order or a bit sequence.
type BitSequenceDecoder struct {
	BitStoreFieldDecoder FieldDecoder
	BitOrderFieldDecoder FieldDecoder
}

const (
	bitStoreKey = "bit_store"
	bitOrderKey = "bit_order"
)

func (b *BitSequenceDecoder) Decode(decoder *scale.Decoder) (any, error) {
	if b.BitStoreFieldDecoder == nil {
		return nil, errors.New("bit store field decoder not found")
	}

	if b.BitOrderFieldDecoder == nil {
		return nil, errors.New("bit order field decoder not found")
	}

	bitStore, err := b.BitStoreFieldDecoder.Decode(decoder)

	if err != nil {
		return nil, fmt.Errorf("couldn't decode bit store: %w", err)
	}

	bitOrder, err := b.BitOrderFieldDecoder.Decode(decoder)

	if err != nil {
		return nil, fmt.Errorf("couldn't decode bit order: %w", err)
	}

	return map[string]any{
		bitStoreKey: bitStore,
		bitOrderKey: bitOrder,
	}, nil
}
