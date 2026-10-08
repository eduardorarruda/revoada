import glob, json
from opentelemetry.proto.collector.trace.v1.trace_service_pb2 import ExportTraceServiceRequest
def val(v):
    k = v.WhichOneof("value")
    if k == "array_value": return [val(x) for x in v.array_value.values]
    if k == "kvlist_value": return {x.key: val(x.value) for x in v.kvlist_value.values}
    return getattr(v, k) if k else None
for f in sorted(glob.glob("out/*.pb")):
    r = ExportTraceServiceRequest(); r.ParseFromString(open(f, "rb").read())
    for rs in r.resource_spans:
        for ss in rs.scope_spans:
            for s in ss.spans:
                a = {kv.key: val(kv.value) for kv in s.attributes}
                ev = [(e.name, {kv.key: val(kv.value) for kv in e.attributes}) for e in s.events]
                print(f"== {f} | {ss.scope.name} | {s.name} | status={s.status.code} {s.status.message[:60]}")
                for k, v in sorted(a.items()):
                    sv = json.dumps(v, ensure_ascii=False)
                    print(f"   {k} = {sv[:160]}")
                for n, ea in ev:
                    print(f"   EVENT {n}: " + json.dumps(ea, ensure_ascii=False)[:300])
