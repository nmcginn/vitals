`/proc` and `/etc` trees, one directory per machine, each read as an `fs.FS`
rooted at `/`. Captured trees are copied verbatim from the machine named.
Hand-written trees follow the kernel's format, with made-up counter values,
and should be replaced by captures when such a machine is to hand.

- `ubuntu-vm/`: captured from an Ubuntu 24.04 x86-64 cloud VM with 4 vCPUs (kernel 6.18).
- `x86-8core/`: hand-written. `proc/stat` and `proc/cpuinfo` for an 8-core,
  8-thread Core i7-9700.
- `arm64-graviton2/`: hand-written. `proc/stat` and `proc/cpuinfo` for a 4-vCPU
  AWS Graviton2 (Neoverse N1), whose cpuinfo has no `model name`.
