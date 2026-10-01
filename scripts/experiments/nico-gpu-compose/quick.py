"""Bounded run requested by the user: one real clip, three repetitions."""
import run as r
import verify as v

v.quality('real','gpu-rgba','gpu-i420','yuv420p')
r.bench('real',r.VARIANTS,3)
v.videos()
