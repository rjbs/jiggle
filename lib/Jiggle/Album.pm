package Jiggle::Album;
use v5.36;

use Moo;

use JSON::MaybeXS ();
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

# Only on albums imported from Flickr: the photoset's id.
has flickr_id   => (is => 'ro');

# When the album was made: a TOML datetime.  Albums are listed newest first.
has created     => (is => 'ro');

sub from_toml_file ($class, $file) {
  my $data = load_toml_file($file);
  my $self = eval {
    $class->new({ %$data, slug => $file->basename(qr/\.toml\z/) });
  };
  die "error loading $file: $@" unless $self;
  return $self;
}

my $JSON = JSON::MaybeXS->new->allow_nonref->canonical;

# See Jiggle::Photo: a JSON string is also a TOML basic string.
sub _str ($s) { $JSON->encode("$s") }

=method as_toml

This returns the album as TOML text, in a fixed order, with one photo id per
line so that reordering an album makes a readable diff.  The slug isn't
included, because it's the file's name.

=cut

sub as_toml ($self) {
  my @lines = sprintf 'title = %s', _str($self->title);

  my $desc = $self->description;
  if ($desc =~ /\n/) {
    # As in Jiggle::Photo: keep a run of quotes from closing the string.
    (my $escaped = $desc) =~ s/\\/\\\\/g;
    $escaped =~ s/"(?=")/\\"/g;
    $escaped =~ s/"\z/\\"/;
    push @lines, qq{description = """\n$escaped"""};
  } else {
    push @lines, sprintf 'description = %s', _str($desc);
  }

  push @lines, sprintf 'created = %s', $self->created if defined $self->created;
  push @lines, sprintf 'cover = %s', _str($self->cover) if defined $self->cover;
  push @lines, sprintf 'flickr_id = %s', _str($self->flickr_id)
    if defined $self->flickr_id;

  push @lines, 'photos = [', (map {; '  ' . _str($_) . ',' } $self->photos->@*), ']';

  return join qq{\n}, @lines, q{};
}

1;
