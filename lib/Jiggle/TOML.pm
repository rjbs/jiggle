package Jiggle::TOML;
use v5.36;

use Exporter 'import';
our @EXPORT_OK = qw( load_toml_file datetime_with_offset );

use POSIX ();
use TOML::Tiny ();

=head1 NAME

Jiggle::TOML - read TOML files the way jiggle wants them

=head1 DESCRIPTION

TOML::Tiny's strict mode, which we want so that malformed metadata is an
error rather than a surprise, produces Math::BigFloat and Math::BigInt objects
for numbers, and JSON::PP::Boolean objects for booleans.  Nothing in a
jiggle library needs big numbers, and plain values are much easier to
compare and serialize, so this reader converts them: booleans become 1 and
0.  Datetimes are left as their string form.

=cut

sub load_toml_file ($file) {
  my $data = eval {
    TOML::Tiny::from_toml(
      $file->slurp_raw,
      strict          => 1,
      inflate_float   => sub ($n) { 0 + $n },
      inflate_integer => sub ($n) { 0 + $n },
      inflate_boolean => sub ($b) { $b eq 'true' ? 1 : 0 },
    );
  };

  die "error parsing $file: $@" unless $data;
  return $data;
}

=func datetime_with_offset

  my $datetime = datetime_with_offset(time);    # 2026-09-30T10:15:00-04:00

This returns an epoch time as a TOML datetime on this machine's clock, with
its UTC offset.

=cut

sub datetime_with_offset ($epoch) {
  my @t = localtime $epoch;
  my $datetime = POSIX::strftime('%Y-%m-%dT%H:%M:%S', @t);
  my $offset   = POSIX::strftime('%z', @t);
  $offset =~ s/\A([-+]\d\d)(\d\d)\z/$1:$2/;
  return "$datetime$offset";
}

1;
