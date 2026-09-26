package Jiggle;
use v5.36;

our $VERSION = '0.001';

1;

=head1 NAME

Jiggle - a static photo site built from plain files

=head1 OVERVIEW

A jiggle library is a directory holding a F<jiggle.toml> configuration file
and three trees: F<originals> (write-once original files), F<meta> (one TOML
file per photo, plus albums), and F<derived> (generated renditions, a cache).
The C<jiggle> command ingests new photos into a library and builds a static
site from it.

See F<PLAN.md> in the distribution for the design.

=cut
