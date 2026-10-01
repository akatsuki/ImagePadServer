"""Follow-up separation of raw pipe and FFmpeg overlay/color conversion."""
from probe import bench
for mode in ('pipe','overlay'):
    bench('real',['baseline','hardware'],mode,3)
