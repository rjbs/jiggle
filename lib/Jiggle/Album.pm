package Jiggle::Album;
use v5.36;

use Moo;

use Jiggle::TOML qw( load_toml_file );

=head1 NAME

Jiggle::Album - an ordered collection of photos

=head1 DESCRIPTION

Albums live in F<meta/albums/SLUG.toml>.  Membership is recorded here, in
order, rather than on the photos, so that reordering an album means editing
one file.

=cut

has slug        => (is => 'ro', required => 1);
has title       => (is => 'ro', required => 1);
has description => (is => 'ro', default  => '');
has cover       => (is => 'ro');
has photos      => (is => 'ro', default  => sub { [] });

sub from_toml_file ($class, $file) {
  my $data = load_toml_file($file);
  my $self = eval {
    $class->new({ %$data, slug => $file->basename(qr/\.toml\z/) });
  };
  die "error loading $file: $@" unless $self;
  return $self;
}

1;
