package crm

import (
	"context"
	"database/sql/driver"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"
)

// registerVectorType teaches a connection's type map the pgvector `vector`
// type so pgvector.Vector and []float32 travel in binary format. It is a
// no-op when the extension is not installed yet (migrations create it); the
// repository casts parameters with ::vector and passes pgvector.Vector, which
// also works through its text driver.Valuer, so queries stay correct either
// way.
func registerVectorType(ctx context.Context, conn *pgx.Conn) error {
	var oid *uint32
	if err := conn.QueryRow(ctx, `SELECT to_regtype('vector')::oid`).Scan(&oid); err != nil {
		return fmt.Errorf("crm: look up vector type: %w", err)
	}
	if oid == nil || *oid == 0 {
		return nil
	}
	conn.TypeMap().RegisterType(&pgtype.Type{Name: "vector", OID: *oid, Codec: vectorCodec{}})
	return nil
}

// vectorCodec encodes and decodes pgvector values (binary and text formats).
type vectorCodec struct{}

func (vectorCodec) FormatSupported(format int16) bool {
	return format == pgtype.BinaryFormatCode || format == pgtype.TextFormatCode
}

func (vectorCodec) PreferredFormat() int16 { return pgtype.BinaryFormatCode }

func (vectorCodec) PlanEncode(_ *pgtype.Map, _ uint32, format int16, value any) pgtype.EncodePlan {
	switch value.(type) {
	case pgvector.Vector, *pgvector.Vector, []float32:
	default:
		return nil
	}
	if format == pgtype.BinaryFormatCode {
		return vectorEncodePlan{binary: true}
	}
	return vectorEncodePlan{}
}

type vectorEncodePlan struct{ binary bool }

func (p vectorEncodePlan) Encode(value any, buf []byte) ([]byte, error) {
	var v pgvector.Vector
	switch x := value.(type) {
	case pgvector.Vector:
		v = x
	case *pgvector.Vector:
		if x == nil {
			return nil, nil
		}
		v = *x
	case []float32:
		if x == nil {
			return nil, nil
		}
		v = pgvector.NewVector(x)
	default:
		return nil, fmt.Errorf("crm: cannot encode %T as vector", value)
	}
	if p.binary {
		return v.EncodeBinary(buf)
	}
	return append(buf, v.String()...), nil
}

func (vectorCodec) PlanScan(_ *pgtype.Map, _ uint32, format int16, target any) pgtype.ScanPlan {
	switch target.(type) {
	case *pgvector.Vector, *[]float32:
		return vectorScanPlan{binary: format == pgtype.BinaryFormatCode}
	}
	return nil
}

type vectorScanPlan struct{ binary bool }

func (p vectorScanPlan) Scan(src []byte, target any) error {
	var v pgvector.Vector
	if src != nil {
		var err error
		if p.binary {
			err = v.DecodeBinary(src)
		} else {
			err = v.Parse(string(src))
		}
		if err != nil {
			return fmt.Errorf("crm: decode vector: %w", err)
		}
	}
	switch t := target.(type) {
	case *pgvector.Vector:
		*t = v
	case *[]float32:
		if src == nil {
			*t = nil
		} else {
			*t = v.Slice()
		}
	default:
		return fmt.Errorf("crm: cannot scan vector into %T", target)
	}
	return nil
}

func (c vectorCodec) DecodeDatabaseSQLValue(m *pgtype.Map, oid uint32, format int16, src []byte) (driver.Value, error) {
	if src == nil {
		return nil, nil
	}
	v, err := c.DecodeValue(m, oid, format, src)
	if err != nil {
		return nil, err
	}
	return v.(pgvector.Vector).String(), nil
}

func (vectorCodec) DecodeValue(_ *pgtype.Map, _ uint32, format int16, src []byte) (any, error) {
	if src == nil {
		return nil, nil
	}
	var v pgvector.Vector
	if err := (vectorScanPlan{binary: format == pgtype.BinaryFormatCode}).Scan(src, &v); err != nil {
		return nil, err
	}
	return v, nil
}
