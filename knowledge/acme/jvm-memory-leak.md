# JVM memory leak and long GC pauses

Owner: platform-runtime.

## Symptoms
- Heap usage (`chaos_memory_mb`) climbs steadily and never returns to baseline after GC.
- Log line: `GC pause exceeded threshold` with pauses above 500 ms.
- Pods restarted with reason `OOMKilled`; latency spikes line up with GC pauses.

## Diagnosis
1. Capture a heap dump before restarting: `jcmd <pid> GC.heap_dump /tmp/heap.hprof`.
2. Open it in Eclipse MAT and check the dominator tree. Unbounded caches and listener registrations are the usual culprits.

## Mitigation
Restart the service to restore capacity (rolling restart, one pod at a time). This only buys time. The leak returns at the same growth rate until the code is fixed.
