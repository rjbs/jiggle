package Jiggle::Geo;
use v5.36;

use Exporter 'import';
our @EXPORT_OK = qw( distance_m in_private_zone );

=head1 NAME

Jiggle::Geo - small geographic helpers

=head1 FUNCTIONS

=head2 distance_m

  my $meters = distance_m($lat1, $lon1, $lat2, $lon2);

This returns the great-circle distance between two points, by the haversine
formula.  At the scale of a private zone (a few hundred meters around a
house) treating the Earth as a sphere is more than accurate enough.

=cut

my $EARTH_RADIUS_M = 6_371_000;
my $RAD = atan2(1, 1) / 45;

sub distance_m ($lat1, $lon1, $lat2, $lon2) {
  my $dlat = ($lat2 - $lat1) * $RAD;
  my $dlon = ($lon2 - $lon1) * $RAD;

  my $h = sin($dlat / 2) ** 2
        + cos($lat1 * $RAD) * cos($lat2 * $RAD) * sin($dlon / 2) ** 2;

  return 2 * $EARTH_RADIUS_M * atan2(sqrt($h), sqrt(1 - $h));
}

=head2 in_private_zone

  if (in_private_zone($location, \@zones)) { ... }

C<$location> is a hash with C<lat> and C<lon>.  Each zone is a hash with
C<lat>, C<lon>, and C<radius> (in meters).  This returns true if the location
falls within any zone.

=cut

sub in_private_zone ($location, $zones) {
  for my $zone (@$zones) {
    my $d = distance_m(@$location{qw( lat lon )}, @$zone{qw( lat lon )});
    return 1 if $d <= $zone->{radius};
  }

  return;
}

1;
