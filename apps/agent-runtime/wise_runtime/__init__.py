"""Wise agent runtime.

The runtime has no database access and no internet access. It talks only to
the Wise control plane: it claims runs, sends transcripts to the Model Gateway,
executes read-only tools locally and reports events and results.
"""

__version__ = "0.1.0"
