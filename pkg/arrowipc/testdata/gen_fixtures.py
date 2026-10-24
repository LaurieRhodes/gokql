"""Writes the Arrow fixtures used by arrowipc's tests, with pyarrow.

Run from this directory: python3 gen_fixtures.py
"""
import datetime as dt
import decimal
import uuid

import pyarrow as pa
import pyarrow.feather as feather
import pyarrow.ipc as ipc

U = uuid.UUID("12345678-1234-5678-9abc-def012345678")

table = pa.table({
    "i8": pa.array([-1, None, 127], pa.int8()),
    "u64": pa.array([1, 2, None], pa.uint64()),
    "i32": pa.array([7, None, -7], pa.int32()),
    "f16": pa.array([1.5, None, -2.0], pa.float16()),
    "f32": pa.array([0.25, 1.0, None], pa.float32()),
    "b": pa.array([True, None, False]),
    "s": pa.array(["a", None, "ccc"]),
    "ls": pa.array(["x", "yy", None], pa.large_string()),
    "bin": pa.array([b"ab", None, b""], pa.binary()),
    "dec": pa.array([decimal.Decimal("-12.345"), None, decimal.Decimal("0.001")], pa.decimal128(10, 3)),
    "d32": pa.array([dt.date(2024, 2, 29), None, dt.date(1969, 12, 31)], pa.date32()),
    "tod": pa.array([dt.time(1, 2, 3, 400000), None, dt.time(23, 59)], pa.time64("us")),
    "ts_s": pa.array([dt.datetime(2024, 1, 1, 12), None, dt.datetime(1960, 6, 1)], pa.timestamp("s")),
    "ts_us_tz": pa.array([dt.datetime(2024, 1, 1, 12, 0, 0, 123456), None, None], pa.timestamp("us", tz="UTC")),
    "dur_ms": pa.array([dt.timedelta(days=1, milliseconds=5), None, dt.timedelta(seconds=-3)], pa.duration("ms")),
    "lst": pa.array([[1, 2], None, []], pa.list_(pa.int64())),
    "st": pa.array([{"k": "v", "n": 1}, None, {"k": None, "n": 3}]),
    "mp": pa.array([[("a", 1), ("b", 2)], None, []], pa.map_(pa.string(), pa.int32())),
    "dict": pa.array(["p", None, "p"]).dictionary_encode(),
    "uid": pa.ExtensionArray.from_storage(pa.uuid(), pa.array([U.bytes, None, U.bytes], pa.binary(16))),
})

feather.write_feather(table, "all_lz4.feather")  # Feather v2, LZ4 by default
feather.write_feather(table, "all_zstd.feather", compression="zstd")
with ipc.new_file("all_plain.arrow", table.schema) as w:
    w.write_table(table)

# a stream of several batches, with a delta dictionary
schema = pa.schema([("tag", pa.dictionary(pa.int8(), pa.string())), ("n", pa.int64())])
with ipc.new_stream("delta.arrows", schema, options=ipc.IpcWriteOptions(emit_dictionary_deltas=True)) as w:
    w.write_batch(pa.record_batch([pa.DictionaryArray.from_arrays(pa.array([0, 1], pa.int8()), pa.array(["a", "b"])), pa.array([1, 2])], schema=schema))
    w.write_batch(pa.record_batch([pa.DictionaryArray.from_arrays(pa.array([2, 0], pa.int8()), pa.array(["a", "b", "c"])), pa.array([3, 4])], schema=schema))

# a sliced table, and an empty one
with ipc.new_stream("sliced.arrows", table.schema) as w:
    w.write_table(table.slice(1, 2))
with ipc.new_file("empty.arrow", pa.schema([("x", pa.int64()), ("s", pa.string())])) as w:
    pass
