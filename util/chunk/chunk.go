// Copyright 2017 PingCAP, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// See the License for the specific language governing permissions and
// limitations under the License.

package chunk

import (
	"unsafe"

	"github.com/pingcap/tidb/mysql"
	"github.com/pingcap/tidb/types"
	"github.com/pingcap/tidb/types/json"
	"github.com/pingcap/tidb/util/hack"
)

var _ types.Row = Row{}

// Chunk stores multiple rows of data in Apache Arrow format.
// See https://arrow.apache.org/docs/memory_layout.html
// Values are appended in compact format and can be directly accessed without decoding.
// When the chunk is done processing, we can reuse the allocated memory by resetting it.
type Chunk struct {
	columns []*column
}

// Capacity constants.
const (
	InitialCapacity = 32
	MaxCapacity     = 1024
)

// NewChunk creates a new chunk with field types.
func NewChunk(fields []*types.FieldType) *Chunk {
	chk := new(Chunk)
	for _, f := range fields {
		chk.addColumnByFieldType(f, InitialCapacity)
	}
	return chk
}

// addFixedLenColumn adds a fixed length column with elemLen and initial data capacity.
func (c *Chunk) addFixedLenColumn(elemLen, initCap int) {
	c.columns = append(c.columns, &column{
		elemBuf: make([]byte, elemLen),
		data:    make([]byte, 0, initCap),
	})
}

// addVarLenColumn adds a variable length column with initial data capacity.
func (c *Chunk) addVarLenColumn(initCap int) {
	c.columns = append(c.columns, &column{
		offsets: []int32{0},
		data:    make([]byte, 0, initCap),
	})
}

// addInterfaceColumn adds an interface column which holds element as interface.
func (c *Chunk) addInterfaceColumn() {
	c.columns = append(c.columns, &column{
		ifaces: []interface{}{},
	})
}

// addColumnByFieldType adds a column by field type.
func (c *Chunk) addColumnByFieldType(fieldTp *types.FieldType, initCap int) {
	switch fieldTp.Tp {
	case mysql.TypeFloat:
		c.addFixedLenColumn(4, initCap)
	case mysql.TypeTiny, mysql.TypeShort, mysql.TypeInt24, mysql.TypeLong, mysql.TypeLonglong,
		mysql.TypeDouble, mysql.TypeYear:
		c.addFixedLenColumn(8, initCap)
	case mysql.TypeDuration:
		c.addFixedLenColumn(16, initCap)
	case mysql.TypeNewDecimal:
		c.addFixedLenColumn(types.MyDecimalStructSize, initCap)
	case mysql.TypeDate, mysql.TypeDatetime, mysql.TypeTimestamp, mysql.TypeJSON:
		c.addInterfaceColumn()
	default:
		c.addVarLenColumn(initCap)
	}
}

// Reset resets the chunk, so the memory it allocated can be reused.
// Make sure all the data in the chunk is not used anymore before you reuse this chunk.
func (c *Chunk) Reset() {
	for _, c := range c.columns {
		c.reset()
	}
}

// NumCols returns the number of columns in the chunk.
func (c *Chunk) NumCols() int {
	return len(c.columns)
}

// NumRows returns the number of rows in the chunk.
func (c *Chunk) NumRows() int {
	if len(c.columns) == 0 {
		return 0
	}
	return c.columns[0].length
}

// GetRow gets the Row in the chunk with the row index.
func (c *Chunk) GetRow(idx int) Row {
	return Row{c: c, idx: idx}
}

// AppendRow appends a row to the chunk.
func (c *Chunk) AppendRow(colIdx int, row Row) {
	for i, rowCol := range row.c.columns {
		chkCol := c.columns[colIdx+i]
		chkCol.setNullBitmap(!rowCol.isNull(row.idx))
		if rowCol.isFixed() {
			elemLen := len(rowCol.elemBuf)
			offset := row.idx * elemLen
			chkCol.data = append(chkCol.data, rowCol.data[offset:offset+elemLen]...)
		} else if rowCol.isVarlen() {
			start, end := rowCol.offsets[row.idx], rowCol.offsets[row.idx+1]
			chkCol.data = append(chkCol.data, rowCol.data[start:end]...)
			chkCol.offsets = append(chkCol.offsets, int32(len(chkCol.data)))
		} else {
			chkCol.ifaces = append(chkCol.ifaces, rowCol.ifaces[row.idx])
		}
		chkCol.length++
	}
}

// AppendNull appends a null value to the chunk.
func (c *Chunk) AppendNull(colIdx int) {
	c.columns[colIdx].appendNull()
}

// AppendInt64 appends a int64 value to the chunk.
func (c *Chunk) AppendInt64(colIdx int, i int64) {
	c.columns[colIdx].appendInt64(i)
}

// AppendUint64 appends a uint64 value to the chunk.
func (c *Chunk) AppendUint64(colIdx int, u uint64) {
	c.columns[colIdx].appendUint64(u)
}

// AppendFloat32 appends a float32 value to the chunk.
func (c *Chunk) AppendFloat32(colIdx int, f float32) {
	c.columns[colIdx].appendFloat32(f)
}

// AppendFloat64 appends a float64 value to the chunk.
func (c *Chunk) AppendFloat64(colIdx int, f float64) {
	c.columns[colIdx].appendFloat64(f)
}

// AppendString appends a string value to the chunk.
func (c *Chunk) AppendString(colIdx int, str string) {
	c.columns[colIdx].appendString(str)
}

// AppendBytes appends a bytes value to the chunk.
func (c *Chunk) AppendBytes(colIdx int, b []byte) {
	c.columns[colIdx].appendBytes(b)
}

// AppendTime appends a Time value to the chunk.
// TODO: change the time structure so it can be directly written to memory.
func (c *Chunk) AppendTime(colIdx int, t types.Time) {
	c.columns[colIdx].appendInterface(t)
}

// AppendDuration appends a Duration value to the chunk.
func (c *Chunk) AppendDuration(colIdx int, dur types.Duration) {
	c.columns[colIdx].appendDuration(dur)
}

// AppendMyDecimal appends a MyDecimal value to the chunk.
func (c *Chunk) AppendMyDecimal(colIdx int, dec *types.MyDecimal) {
	c.columns[colIdx].appendMyDecimal(dec)
}

// AppendEnum appends an Enum value to the chunk.
func (c *Chunk) AppendEnum(colIdx int, enum types.Enum) {
	c.columns[colIdx].appendNameValue(enum.Name, enum.Value)
}

// AppendSet appends a Set value to the chunk.
func (c *Chunk) AppendSet(colIdx int, set types.Set) {
	c.columns[colIdx].appendNameValue(set.Name, set.Value)
}

// AppendJSON appends a JSON value to the chunk.
func (c *Chunk) AppendJSON(colIdx int, j json.JSON) {
	c.columns[colIdx].appendInterface(j)
}

type column struct {
	length     int
	nullCount  int
	nullBitmap []byte
	offsets    []int32
	data       []byte
	elemBuf    []byte
	ifaces     []interface{}
}

func (c *column) isFixed() bool {
	return c.elemBuf != nil
}

func (c *column) isVarlen() bool {
	return c.offsets != nil
}

func (c *column) isInterface() bool {
	return c.ifaces != nil
}

func (c *column) reset() {
	c.length = 0
	c.nullCount = 0
	c.nullBitmap = c.nullBitmap[:0]
	if len(c.offsets) > 0 {
		// The first offset is always 0, it makes slicing the data easier, we need to keep it.
		c.offsets = c.offsets[:1]
	}
	c.data = c.data[:0]
	c.ifaces = c.ifaces[:0]
}

func (c *column) isNull(rowIdx int) bool {
	nullByte := c.nullBitmap[rowIdx/8]
	return nullByte&(1<<(uint(rowIdx)&7)) == 0
}

func (c *column) setNullBitmap(on bool) {
	idx := c.length >> 3
	if idx >= len(c.nullBitmap) {
		c.nullBitmap = append(c.nullBitmap, 0)
	}
	if on {
		pos := uint(c.length) & 7
		c.nullBitmap[idx] |= byte((1 << pos))
	} else {
		c.nullCount++
	}
}

func (c *column) appendNull() {
	c.setNullBitmap(false)
	if c.isFixed() {
		c.data = append(c.data, c.elemBuf...)
	} else if c.isVarlen() {
		c.offsets = append(c.offsets, c.offsets[c.length])
	} else {
		c.ifaces = append(c.ifaces, nil)
	}
	c.length++
}

func (c *column) finishAppendFixed() {
	c.data = append(c.data, c.elemBuf...)
	c.setNullBitmap(true)
	c.length++
}

func (c *column) appendInt64(i int64) {
	*(*int64)(unsafe.Pointer(&c.elemBuf[0])) = i
	c.finishAppendFixed()
}

func (c *column) appendUint64(u uint64) {
	*(*uint64)(unsafe.Pointer(&c.elemBuf[0])) = u
	c.finishAppendFixed()
}

func (c *column) appendFloat32(f float32) {
	*(*float32)(unsafe.Pointer(&c.elemBuf[0])) = f
	c.finishAppendFixed()
}

func (c *column) appendFloat64(f float64) {
	*(*float64)(unsafe.Pointer(&c.elemBuf[0])) = f
	c.finishAppendFixed()
}

func (c *column) finishAppendVar() {
	c.setNullBitmap(true)
	c.offsets = append(c.offsets, int32(len(c.data)))
	c.length++
}

func (c *column) appendString(str string) {
	c.data = append(c.data, str...)
	c.finishAppendVar()
}

func (c *column) appendBytes(b []byte) {
	c.data = append(c.data, b...)
	c.finishAppendVar()
}

func (c *column) appendInterface(o interface{}) {
	c.ifaces = append(c.ifaces, o)
	c.setNullBitmap(true)
	c.length++
}

func (c *column) appendDuration(dur types.Duration) {
	*(*types.Duration)(unsafe.Pointer(&c.elemBuf[0])) = dur
	c.finishAppendFixed()
}

func (c *column) appendMyDecimal(dec *types.MyDecimal) {
	*(*types.MyDecimal)(unsafe.Pointer(&c.elemBuf[0])) = *dec
	c.finishAppendFixed()
}

func (c *column) appendNameValue(name string, val uint64) {
	var buf [8]byte
	*(*uint64)(unsafe.Pointer(&buf[0])) = val
	c.data = append(c.data, buf[:]...)
	c.data = append(c.data, name...)
	c.finishAppendVar()
}

// Row represents a row of data, can be used to assess values.
type Row struct {
	c   *Chunk
	idx int
}

// Len returns the number of values in the row.
func (r Row) Len() int {
	return r.c.NumCols()
}

// GetInt64 returns the int64 value with the colIdx.
func (r Row) GetInt64(colIdx int) int64 {
	col := r.c.columns[colIdx]
	return *(*int64)(unsafe.Pointer(&col.data[r.idx*8]))
}

// GetUint64 returns the uint64 value with the colIdx.
func (r Row) GetUint64(colIdx int) uint64 {
	col := r.c.columns[colIdx]
	return *(*uint64)(unsafe.Pointer(&col.data[r.idx*8]))
}

// GetFloat32 returns the float64 value with the colIdx.
func (r Row) GetFloat32(colIdx int) float32 {
	col := r.c.columns[colIdx]
	return *(*float32)(unsafe.Pointer(&col.data[r.idx*4]))
}

// GetFloat64 returns the float64 value with the colIdx.
func (r Row) GetFloat64(colIdx int) float64 {
	col := r.c.columns[colIdx]
	return *(*float64)(unsafe.Pointer(&col.data[r.idx*8]))
}

// GetString returns the string value with the colIdx.
func (r Row) GetString(colIdx int) string {
	col := r.c.columns[colIdx]
	start, end := col.offsets[r.idx], col.offsets[r.idx+1]
	return hack.String(col.data[start:end])
}

// GetBytes returns the bytes value with the colIdx.
func (r Row) GetBytes(colIdx int) []byte {
	col := r.c.columns[colIdx]
	start, end := col.offsets[r.idx], col.offsets[r.idx+1]
	return col.data[start:end]
}

// GetTime returns the Time value with the colIdx.
func (r Row) GetTime(colIdx int) types.Time {
	col := r.c.columns[colIdx]
	t, _ := col.ifaces[r.idx].(types.Time)
	return t
}

// GetDuration returns the Duration value with the colIdx.
func (r Row) GetDuration(colIdx int) types.Duration {
	col := r.c.columns[colIdx]
	return *(*types.Duration)(unsafe.Pointer(&col.data[r.idx*16]))
}

func (r Row) getNameValue(colIdx int) (string, uint64) {
	col := r.c.columns[colIdx]
	start, end := col.offsets[r.idx], col.offsets[r.idx+1]
	if start == end {
		return "", 0
	}
	val := *(*uint64)(unsafe.Pointer(&col.data[start]))
	name := hack.String(col.data[start+8 : end])
	return name, val
}

// GetEnum returns the Enum value with the colIdx.
func (r Row) GetEnum(colIdx int) types.Enum {
	name, val := r.getNameValue(colIdx)
	return types.Enum{Name: name, Value: val}
}

// GetSet returns the Set value with the colIdx.
func (r Row) GetSet(colIdx int) types.Set {
	name, val := r.getNameValue(colIdx)
	return types.Set{Name: name, Value: val}
}

// GetMyDecimal returns the MyDecimal value with the colIdx.
func (r Row) GetMyDecimal(colIdx int) *types.MyDecimal {
	col := r.c.columns[colIdx]
	return (*types.MyDecimal)(unsafe.Pointer(&col.data[r.idx*types.MyDecimalStructSize]))
}

// GetJSON returns the JSON value with the colIdx.
func (r Row) GetJSON(colIdx int) json.JSON {
	col := r.c.columns[colIdx]
	j, _ := col.ifaces[r.idx].(json.JSON)
	return j
}

// GetDatum implements the types.Row interface.
func (r Row) GetDatum(colIdx int, tp *types.FieldType) types.Datum {
	var d types.Datum
	switch tp.Tp {
	case mysql.TypeTiny, mysql.TypeShort, mysql.TypeInt24, mysql.TypeLong, mysql.TypeLonglong, mysql.TypeYear:
		if !r.IsNull(colIdx) {
			if mysql.HasUnsignedFlag(tp.Flag) {
				d.SetUint64(r.GetUint64(colIdx))
			} else {
				d.SetInt64(r.GetInt64(colIdx))
			}
		}
	case mysql.TypeFloat:
		if !r.IsNull(colIdx) {
			d.SetFloat32(r.GetFloat32(colIdx))
		}
	case mysql.TypeDouble:
		if !r.IsNull(colIdx) {
			d.SetFloat64(r.GetFloat64(colIdx))
		}
	case mysql.TypeVarchar, mysql.TypeVarString, mysql.TypeString,
		mysql.TypeBlob, mysql.TypeTinyBlob, mysql.TypeMediumBlob, mysql.TypeLongBlob:
		if !r.IsNull(colIdx) {
			d.SetBytes(r.GetBytes(colIdx))
		}
	case mysql.TypeDate, mysql.TypeDatetime, mysql.TypeTimestamp:
		if !r.IsNull(colIdx) {
			d.SetMysqlTime(r.GetTime(colIdx))
		}
	case mysql.TypeDuration:
		if !r.IsNull(colIdx) {
			d.SetMysqlDuration(r.GetDuration(colIdx))
		}
	case mysql.TypeNewDecimal:
		if !r.IsNull(colIdx) {
			d.SetMysqlDecimal(r.GetMyDecimal(colIdx))
		}
	case mysql.TypeEnum:
		if !r.IsNull(colIdx) {
			d.SetMysqlEnum(r.GetEnum(colIdx))
		}
	case mysql.TypeSet:
		if !r.IsNull(colIdx) {
			d.SetMysqlSet(r.GetSet(colIdx))
		}
	case mysql.TypeBit:
		if !r.IsNull(colIdx) {
			d.SetMysqlBit(r.GetBytes(colIdx))
		}
	case mysql.TypeJSON:
		if !r.IsNull(colIdx) {
			d.SetMysqlJSON(r.GetJSON(colIdx))
		}
	}
	return d
}

// IsNull implements the types.Row interface.
func (r Row) IsNull(colIdx int) bool {
	return r.c.columns[colIdx].isNull(r.idx)
}
