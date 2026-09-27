package Jiggle::Ingest;
use v5.36;

use Moo;

use Digest::SHA ();
use File::Copy ();
use Image::ExifTool ();
use Jiggle::Photo;
use Path::Tiny ();

=head1 NAME

Jiggle::Ingest - bring new original files into a library

=head1 SYNOPSIS

  my $ingest = Jiggle::Ingest->new({ library => $library });
  my @photos = $ingest->ingest_files(@paths);

=head1 DESCRIPTION

Ingesting a file means: hashing it, deriving its id from the hash, skipping it
if that id is already in the library, copying it into the originals tree, and
writing a stub metadata file with the facts found in its EXIF data.

Because the id comes from the file's content, checking for a duplicate means
looking for one metadata file, not loading the whole library.  If a different
file has the same id (a collision in the abbreviated hash), ingest dies.  At
48 bits that should never happen, and if it does, a person should decide what
to do.

Ingest never modifies or removes the source files.

=cut

has library => (is => 'ro', required => 1);

# Called with a message for each file skipped or ingested.
has logger => (is => 'ro', default => sub { sub { } });

# These are the types we can make renditions of today.  Video comes later.
# -- claude, 2026-09-26
my %EXT_FOR_TYPE = (
  JPEG => 'jpg',
  PNG  => 'png',
  HEIC => 'heic',
  WEBP => 'webp',
);

=method ingest_files

  my @photos = $ingest->ingest_files(@paths);

This ingests each file and returns the L<Jiggle::Photo> objects created.
Files already in the library (by digest) and files of unsupported types are
skipped, with a message sent to the logger.

=cut

sub ingest_files ($self, @paths) {
  my @photos;

  for my $path (map {; Path::Tiny::path($_) } @paths) {
    my $digest = Digest::SHA->new(256)->addfile("$path")->hexdigest;
    my $id     = $self->library->id_for_digest($digest);

    if (-e (my $meta = $self->library->meta_path($id))) {
      my $existing = Jiggle::Photo->from_toml_file($meta);

      die "id collision: $path and photo $id have different digests\n"
        unless $existing->sha256 eq $digest;

      $self->logger->("skip $path: already in library as $id");
      next;
    }

    my $facts = $self->_facts_for($path);

    my $ext = $EXT_FOR_TYPE{ $facts->{type} // '' };
    unless ($ext) {
      $self->logger->("skip $path: unsupported type " . ($facts->{type} // 'unknown'));
      next;
    }

    my $photo = Jiggle::Photo->new({
      id    => $id,
      taken => $facts->{taken},
      ($facts->{location} ? (location => $facts->{location}) : ()),
      original => {
        file   => $path->basename,
        ext    => $ext,
        sha256 => $digest,
        bytes  => -s $path,
        width  => $facts->{width},
        height => $facts->{height},
      },
    });

    $self->_install_original($path, $photo);
    $self->library->add_photo($photo);

    $self->logger->("ingest $path as " . $photo->id);
    push @photos, $photo;
  }

  return @photos;
}

sub _install_original ($self, $source, $photo) {
  my $dest = $self->library->original_path($photo);
  $dest->parent->mkdir;

  # Copy to a temporary name and rename, so an interrupted ingest never
  # leaves a truncated file at a path that looks finished.
  my $tmp = $dest->sibling('.' . $dest->basename . '.tmp');
  File::Copy::copy("$source", "$tmp") or die "can't copy $source to $tmp: $!";
  chmod 0444, "$tmp";
  rename "$tmp", "$dest" or die "can't rename $tmp to $dest: $!";

  return;
}

sub _facts_for ($self, $path) {
  my $exif = Image::ExifTool->new;
  $exif->Options(PrintConv => 0);
  $exif->ExtractInfo("$path");

  my $tag = sub ($name) { $exif->GetValue($name) };

  my %facts = (type => $tag->('FileType'));

  my ($w, $h) = ($tag->('ImageWidth'), $tag->('ImageHeight'));
  ($w, $h) = ($h, $w) if ($tag->('Orientation') // 1) >= 5;
  @facts{qw( width height )} = ($w, $h);

  if (my $dt = $tag->('DateTimeOriginal') // $tag->('CreateDate')) {
    if (my ($y, $m, $d, $time) = $dt =~ /\A(\d{4}):(\d\d):(\d\d) (\d\d:\d\d:\d\d)/) {
      my $offset = $tag->('OffsetTimeOriginal') // $tag->('OffsetTime') // '';
      $offset = '' unless $offset =~ /\A[-+]\d\d:\d\d\z/;
      $facts{taken} = "$y-$m-${d}T$time$offset";
    }
  }

  my ($lat, $lon) = ($tag->('GPS:GPSLatitude'), $tag->('GPS:GPSLongitude'));
  if (defined $lat and defined $lon) {
    # The GPS-group tags are unsigned; the Ref tags say which hemisphere.  We
    # name the group because an unqualified GPSLatitude may be the (signed)
    # Composite tag instead, and negating that would flip it back.
    # -- claude, 2026-09-26
    $lat = -$lat if ($tag->('GPSLatitudeRef')  // '') eq 'S';
    $lon = -$lon if ($tag->('GPSLongitudeRef') // '') eq 'W';
    $facts{location} = { lat => 0 + $lat, lon => 0 + $lon };
  }

  return \%facts;
}

1;
