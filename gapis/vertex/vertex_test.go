// Copyright (C) 2017 Google Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vertex_test

import (
	"math"
	"testing"

	"github.com/google/gapid/core/assert"
	"github.com/google/gapid/core/log"
	"github.com/google/gapid/core/stream"
	. "github.com/google/gapid/core/stream/fmts"
	"github.com/google/gapid/gapis/vertex"
)

var (
	X = stream.Channel_X
	Y = stream.Channel_Y
	Z = stream.Channel_Z
	W = stream.Channel_W
)

func swizzle(f *stream.Format, s ...stream.Channel) *stream.Format {
	out, err := f.Swizzle(s...)
	if err != nil {
		panic(err)
	}
	return out
}

func TestVertexConvert(t *testing.T) {
	ctx := log.Testing(t)
	for _, test := range []struct {
		fmt      *stream.Format
		data     []byte
		expected []float64
	}{

		{
			XYZW_F64,
			[]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x14, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1c, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x20, 0x40,
			}, []float64{
				1, 2, 3, 4,
				5, 6, 7, 8,
			},
		}, {
			swizzle(XYZW_F64, W, Z, Y, X),
			[]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x14, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x18, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x1c, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x20, 0x40,
			}, []float64{
				4, 3, 2, 1,
				8, 7, 6, 5,
			},
		}, {
			XY_F64,
			[]byte{
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xf0, 0x3f,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x08, 0x40,
				0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x10, 0x40,
			}, []float64{
				1, 2, 0, 1,
				3, 4, 0, 1,
			},
		}, {
			XYZW_F32,
			[]byte{
				0x00, 0x00, 0x80, 0x3f,
				0x00, 0x00, 0x00, 0x40,
				0x00, 0x00, 0x40, 0x40,
				0x00, 0x00, 0x80, 0x40,
				0x00, 0x00, 0xa0, 0x40,
				0x00, 0x00, 0xc0, 0x40,
				0x00, 0x00, 0xe0, 0x40,
				0x00, 0x00, 0x00, 0x41,
			}, []float64{
				1, 2, 3, 4,
				5, 6, 7, 8,
			},
		}, {
			swizzle(XYZW_F32, W, Z, Y, X),
			[]byte{
				0x00, 0x00, 0x80, 0x3f,
				0x00, 0x00, 0x00, 0x40,
				0x00, 0x00, 0x40, 0x40,
				0x00, 0x00, 0x80, 0x40,
				0x00, 0x00, 0xa0, 0x40,
				0x00, 0x00, 0xc0, 0x40,
				0x00, 0x00, 0xe0, 0x40,
				0x00, 0x00, 0x00, 0x41,
			}, []float64{
				4, 3, 2, 1,
				8, 7, 6, 5,
			},
		}, {
			XY_F32,
			[]byte{
				0x00, 0x00, 0x80, 0x3f,
				0x00, 0x00, 0x00, 0x40,
				0x00, 0x00, 0x40, 0x40,
				0x00, 0x00, 0x80, 0x40,
			}, []float64{
				1, 2, 0, 1,
				3, 4, 0, 1,
			},
		},
		// TODO:
		//		{
		//			&vertex.FmtFixed16_16 WZYX
		//			[]byte{
		//				0x00, 0x80, 0x01, 0x00,
		//				0x00, 0x80, 0x02, 0x00,
		//				0xff, 0xff, 0xff, 0x7f,
		//				0xff, 0xff, 0xff, 0xff
		//			}, []float64{
		//				-32767.99998474121, 32767.99998474121, 2.5, 1.5,
		//			},
		{
			swizzle(XYZW_U8, W, Z, Y, X),
			[]byte{
				0x00, 0x10, 0x20, 0x30,
			}, []float64{
				48, 32, 16, 0,
			},
		}, {
			swizzle(XYZW_U8_NORM, W, Z, Y, X),
			[]byte{
				0x00, 0x66, 0xbb, 0xff,
			}, []float64{
				1, 0xbb / 255., 0x66 / 255., 0,
			},
		}, {
			swizzle(XYZW_U16, W, Z, Y, X),
			[]byte{
				0x00, 0x00, 0x00, 0x10, 0x00, 0x20, 0x00, 0x30,
			}, []float64{
				12288, 8192, 4096, 0,
			},
		}, {
			swizzle(XYZW_U16_NORM, W, Z, Y, X),
			[]byte{
				0x00, 0x00, 0x66, 0x66, 0xbb, 0xbb, 0xff, 0xff,
			}, []float64{
				1, 0xbbbb / 65535., 0x6666 / 65535., 0,
			},
		}, {
			swizzle(XYZW_U32, W, Z, Y, X),
			[]byte{
				0x00, 0x00, 0x00, 0x00,
				0x00, 0x00, 0x00, 0x10,
				0x00, 0x00, 0x00, 0x20,
				0x00, 0x00, 0x00, 0x30,
			}, []float64{
				805306368, 536870912, 268435456, 0,
			},
		}, {
			swizzle(XYZW_U32_NORM, W, Z, Y, X),
			[]byte{
				0x00, 0x00, 0x00, 0x00,
				0x66, 0x66, 0x66, 0x66,
				0xbb, 0xbb, 0xbb, 0xbb,
				0xff, 0xff, 0xff, 0xff,
			}, []float64{
				1, 0xbbbbbbbb / 4294967295., 0x66666666 / 4294967295., 0,
			},
		}, {
			swizzle(XYZW_S8, W, Z, Y, X),
			[]byte{
				0x80, 0xff, 0x01, 0x7f,
			}, []float64{
				127, 1, -1, -128,
			},
		}, {
			swizzle(XYZW_S8_NORM, W, Z, Y, X),
			[]byte{
				0x80, 0xff, 0x01, 0x7f,
			}, []float64{
				1, 2 * ((128+1.)/255 - .5), 2 * ((128-1.)/255 - .5), -1,
			},
		}, {
			swizzle(XYZW_S16, W, Z, Y, X),
			[]byte{
				0x01, 0x80, 0xff, 0xff, 0x01, 0x00, 0xff, 0x7f,
			}, []float64{
				32767, 1, -1, -32767,
			},
		}, {
			swizzle(XYZW_S16_NORM, W, Z, Y, X),
			[]byte{
				0x01, 0x80, 0xff, 0xff, 0x01, 0x00, 0xff, 0x7f,
			}, []float64{
				1, 1 / 32767., -1 / 32767., -1,
			},
		}, {
			swizzle(XYZW_S32, W, Z, Y, X),
			[]byte{
				0x01, 0x00, 0x00, 0x80,
				0xff, 0xff, 0xff, 0xff,
				0x01, 0x00, 0x00, 0x00,
				0xff, 0xff, 0xff, 0x7f,
			}, []float64{
				2147483647, 1, -1, -2147483647,
			},
		}, {
			swizzle(XYZW_S32_NORM, W, Z, Y, X),
			[]byte{
				0x01, 0x00, 0x00, 0x80,
				0xff, 0xff, 0xff, 0xff,
				0x01, 0x00, 0x00, 0x00,
				0xff, 0xff, 0xff, 0x7f,
			}, []float64{
				1, 1 / 2147483647., -1 / 2147483647., -1,
			},
		}, {
			XYZW_U10U10U10U2,
			[]byte{
				0x00, 0x00, 0x00, 0x00,
				0xff, 0x03, 0x00, 0x00,
				0x00, 0xfc, 0x0f, 0x00,
				0x00, 0x00, 0xf0, 0x3f,
				0x00, 0x00, 0x00, 0xc0,
			}, []float64{
				0, 0, 0, 0,
				1023, 0, 0, 0,
				0, 1023, 0, 0,
				0, 0, 1023, 0,
				0, 0, 0, 3,
			},
		}, {
			XYZW_S10S10S10S2,
			[]byte{
				0x00, 0x00, 0x00, 0x00,
				0x00, 0x02, 0x00, 0x00,
				0x00, 0x00, 0x08, 0x00,
				0x00, 0x00, 0x00, 0x20,
				0x00, 0x00, 0x00, 0x80,
				0xff, 0x03, 0x00, 0x00,
				0x00, 0xfc, 0x0f, 0x00,
				0x00, 0x00, 0xf0, 0x3f,
				0x00, 0x00, 0x00, 0xc0,
				0xff, 0x01, 0x00, 0x00,
				0x00, 0xfc, 0x07, 0x00,
				0x00, 0x00, 0xf0, 0x1f,
				0x00, 0x00, 0x00, 0x40,
			}, []float64{
				0, 0, 0, 0,
				-512, 0, 0, 0,
				0, -512, 0, 0,
				0, 0, -512, 0,
				0, 0, 0, -2,
				-1, 0, 0, 0,
				0, -1, 0, 0,
				0, 0, -1, 0,
				0, 0, 0, -1,
				511, 0, 0, 0,
				0, 511, 0, 0,
				0, 0, 511, 0,
				0, 0, 0, 1,
			},
		}, {
			XYZW_U10U10U10U2_NORM,
			[]byte{
				0x00, 0x00, 0x00, 0x00,
				0xff, 0x03, 0x00, 0x00,
				0x00, 0xfc, 0x0f, 0x00,
				0x00, 0x00, 0xf0, 0x3f,
				0x00, 0x00, 0x00, 0xc0,
			}, []float64{
				0, 0, 0, 0,
				1, 0, 0, 0,
				0, 1, 0, 0,
				0, 0, 1, 0,
				0, 0, 0, 1,
			},
		}, {
			XYZW_S10S10S10S2_NORM,
			[]byte{
				0x00, 0x00, 0x00, 0x00, // [0-3]
				0x00, 0x02, 0x00, 0x00, // [4-7]
				0x00, 0x00, 0x08, 0x00, // [8-11]
				0x00, 0x00, 0x00, 0x20, // [12-15]
				0x00, 0x00, 0x00, 0x80, // [16-19]
				0xff, 0x01, 0x00, 0x00, // [20-23]
				0x00, 0xfc, 0x07, 0x00, // [24-27]
				0x00, 0x00, 0xf0, 0x1f, // [28-31]
				0x00, 0x00, 0x00, 0x40, // [32-35]
			}, []float64{
				0, 0, 0, 2 * ((2+0.)/3 - .5),
				-1, 0, 0, 2 * ((2+0.)/3 - .5),
				0, -1, 0, 2 * ((2+0.)/3 - .5),
				0, 0, -1, 2 * ((2+0.)/3 - .5),
				0, 0, 0, -1,
				1, 0, 0, 2 * ((2+0.)/3 - .5),
				0, 1, 0, 2 * ((2+0.)/3 - .5),
				0, 0, 1, 2 * ((2+0.)/3 - .5),
				0, 0, 0, 1,
			},
		},
	} {
		buffer := &vertex.Buffer{
			Streams: []*vertex.Stream{
				{
					Name:     "stream",
					Data:     test.data,
					Format:   test.fmt,
					Semantic: &vertex.Semantic{Type: vertex.Semantic_Position},
				},
			},
		}
		format := &vertex.BufferFormat{
			Streams: []*vertex.StreamFormat{
				{
					Format:   XYZW_F64,
					Semantic: &vertex.Semantic{Type: vertex.Semantic_Position},
				},
			},
		}
		ctx := log.V{"fmt": test.fmt}.Bind(ctx)
		conv, err := buffer.ConvertTo(ctx, format)
		if assert.For(ctx, "err").ThatError(err).Succeeded() {
			data := conv.Streams[0].Data
			for i, e := range test.expected {
				ctx := log.V{"i": i}.Bind(ctx)
				word := uint64(data[0]) |
					(uint64(data[1]) << 8) |
					(uint64(data[2]) << 16) |
					(uint64(data[3]) << 24) |
					(uint64(data[4]) << 32) |
					(uint64(data[5]) << 40) |
					(uint64(data[6]) << 48) |
					(uint64(data[7]) << 56)
				data = data[8:]
				got := math.Float64frombits(word)
				assert.For(ctx, "got").ThatFloat(got).Equals(e, 0.001)
			}
		}
	}
}
