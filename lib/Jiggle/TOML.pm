package Jiggle::TOML;
use v5.36;

use Exporter 'import';
our @EXPORT_OK = qw( load_toml_file );

use TOML::Tiny ();

=head1 NAME

Jiggle::TOML - read TOML files the way jiggle wants them

=head1 DESCRIPTION

TOML::Tiny's strict mode, which we want so that malformed metadata is an
error rather than a surprise, produces Math::BigFloat and Math::BigInt objects
for numbers.  Nothing in a jiggle library needs that precision, and plain
numbers are much easier to compare and serialize, so this reader converts
them.  Datetimes are left as their string form.

=cut

sub load_toml_file ($file) {
  my $data = eval {
    TOML::Tiny::from_toml(
      $file->slurp_raw,
      strict          => 1,
      inflate_float   => sub ($n) { 0 + $n },
      inflate_integer => sub ($n) { 0 + $n },
    );
  };

  die "error parsing $file: $@" unless $data;
  return $data;
}

1;
