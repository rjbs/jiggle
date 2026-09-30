package Jiggle::Photo;
use v5.36;

use Moo;

use Carp ();
use JSON::MaybeXS ();
use Jiggle::TOML qw( load_toml_file );

=head1 NAME

Jiggle::Photo - one photo (or video), as described by its metadata file

=head1 DESCRIPTION

A photo is built from a TOML file in the library's F<meta> tree.  Most of its
attributes are things a person edits: title, description, tags, visibility.
The C<original> hash records immutable facts about the original file (its
digest, size, and oriented dimensions) so that building pages never requires
opening an image.

=cut

has id    => (is => 'ro', required => 1);
has type  => (
  is  => 'ro',
  default => 'photo',
  isa => sub ($t) {
    Carp::croak("unknown type $t") unless $t eq 'photo' or $t eq 'video';
  },
);

has title       => (is => 'ro', default => '');
has description => (is => 'ro', default => '');

# A TOML datetime, kept as its string form: either with an offset
# (2026-07-17T17:23:17+02:00) or local (2026-07-17T17:23:17).  May be absent.
has taken => (is => 'ro');

has tags  => (is => 'ro', default => sub { [] });

has visibility => (
  is  => 'ro',
  default => 'public',
  isa => sub ($v) {
    Carp::croak("unknown visibility $v") unless $v eq 'public' or $v eq 'private';
  },
);

# True for a photo nobody has reviewed yet: ingest sets it, and the editor
# clears it.  The build never publishes a pending photo, whatever its
# visibility, so a new photo can default to public without going out before
# anyone has looked at it.  -- claude, 2026-09-30
has pending => (is => 'ro', default => 0, coerce => sub ($v) { $v ? 1 : 0 });

has flickr_id => (is => 'ro');

# When the photo was uploaded to Flickr: a TOML datetime, with offset.  Only
# imported photos have one.  It's provenance, and a last-resort date.
has flickr_uploaded => (is => 'ro');

# When the photo was added to the collection: a TOML datetime, with offset.
# Ingest sets it, and imports set it to the Flickr upload time.  It's what
# the feed sorts by.
has added => (is => 'ro');

=method added_at

This returns when the photo joined the collection: C<added>, or, for photos
imported before there was such a thing, C<flickr_uploaded>, which means the
same.  It may be undef.

=cut

sub added_at ($self) { $self->added // $self->flickr_uploaded }

# { file, ext, sha256, bytes, width, height }, plus duration (in seconds) for
# a video, and source_mtime: the modification time of the file ingest copied
# from, as a TOML datetime with the ingesting machine's UTC offset.  Nothing
# uses source_mtime yet; it's kept because it's lost once the source file is
# gone, and Image Capture sets it to the photo's time in the phone's library.
has original => (is => 'ro', required => 1);

# { lat, lon } or undef.  It may also have private = true, meaning the location
# must never be published (say, because it was private on Flickr), though it
# may be used locally.
has location => (is => 'ro');

sub is_public ($self) { $self->visibility eq 'public' }

=method is_published

This is true if the photo belongs on the site: public, and not pending.

=cut

sub is_published ($self) { $self->is_public && ! $self->pending }

sub ext    ($self) { $self->original->{ext}    }
sub sha256 ($self) { $self->original->{sha256} }
# Degrees to turn the picture clockwise, after the correction its own EXIF
# orientation calls for: a photo rotated by hand on Flickr, say.  Usually 0.
has rotate => (
  is  => 'ro',
  default => 0,
  isa => sub ($r) {
    Carp::croak("rotate must be 0, 90, 180, or 270, not $r")
      unless $r == 0 or $r == 90 or $r == 180 or $r == 270;
  },
);

# The dimensions as displayed: the original's (already upright by its EXIF
# orientation), turned by any extra rotation.
sub width ($self) {
  $self->original->{ $self->rotate % 180 ? 'height' : 'width' };
}

sub height ($self) {
  $self->original->{ $self->rotate % 180 ? 'width' : 'height' };
}
sub duration ($self) { $self->original->{duration} }

sub is_video ($self) { $self->type eq 'video' }

sub from_toml_file ($class, $file) {
  my $data = load_toml_file($file);
  my $self = eval { $class->new($data) };
  die "error loading $file: $@" unless $self;
  return $self;
}

=method as_toml

This returns the photo's metadata as TOML text.  Keys come out in a fixed,
readable order rather than whatever order a generic serializer picks, because
these files are meant to be read and edited by hand, and diffed in git.

=cut

my $JSON = JSON::MaybeXS->new->allow_nonref->canonical;

# A JSON string literal is also a valid TOML basic string, since TOML's
# escapes are a superset of the ones JSON emits.  (JSON::MaybeXS doesn't escape
# "/", which would be the one exception.) -- claude, 2026-09-26
sub _str ($s) { $JSON->encode("$s") }

sub as_toml ($self) {
  my @lines;

  push @lines, sprintf 'id = %s',   _str($self->id);
  push @lines, sprintf 'type = %s', _str($self->type);
  push @lines, sprintf 'title = %s', _str($self->title);

  my $desc = $self->description;
  if ($desc =~ /\n/) {
    # Multi-line strings in TOML end at the first """, so escape any run of
    # quotes that could close the string early.
    (my $escaped = $desc) =~ s/\\/\\\\/g;
    $escaped =~ s/"(?=")/\\"/g;
    $escaped =~ s/"\z/\\"/;
    push @lines, qq{description = """\n$escaped"""};
  } else {
    push @lines, sprintf 'description = %s', _str($desc);
  }

  push @lines, sprintf 'taken = %s', $self->taken if defined $self->taken;

  push @lines, sprintf 'tags = [%s]',
    join q{, }, map {; _str($_) } $self->tags->@*;

  push @lines, sprintf 'visibility = %s', _str($self->visibility);
  push @lines, 'pending = true' if $self->pending;
  push @lines, sprintf 'rotate = %d', $self->rotate if $self->rotate;
  push @lines, sprintf 'flickr_id = %s', _str($self->flickr_id)
    if defined $self->flickr_id;
  push @lines, sprintf 'added = %s', $self->added if defined $self->added;
  push @lines, sprintf 'flickr_uploaded = %s', $self->flickr_uploaded
    if defined $self->flickr_uploaded;

  push @lines, q{}, '[original]';
  for my $key (qw( file ext sha256 )) {
    push @lines, sprintf '%s = %s', $key, _str($self->original->{$key});
  }
  for my $key (qw( bytes width height )) {
    push @lines, sprintf '%s = %d', $key, $self->original->{$key};
  }
  push @lines, sprintf 'duration = %.3f', $self->duration
    if defined $self->duration;
  push @lines, sprintf 'source_mtime = %s', $self->original->{source_mtime}
    if defined $self->original->{source_mtime};

  if (my $loc = $self->location) {
    push @lines, q{}, '[location]';
    push @lines, sprintf 'lat = %.7f', $loc->{lat};
    push @lines, sprintf 'lon = %.7f', $loc->{lon};
    push @lines, 'private = true' if $loc->{private};
  }

  return join qq{\n}, @lines, q{};
}

1;
