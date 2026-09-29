package Jiggle::Ingest;
use v5.36;

use Moo;

use Digest::SHA ();
use File::Copy ();
use Image::ExifTool ();
use Jiggle::Photo;
use POSIX ();
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

# The file types we know how to make renditions of, keyed by ExifTool's
# FileType, giving the kind of media and the extension used in the library.
my %MEDIA_FOR_TYPE = (
  JPEG => [ photo => 'jpg'  ],
  PNG  => [ photo => 'png'  ],
  HEIC => [ photo => 'heic' ],
  WEBP => [ photo => 'webp' ],
  GIF  => [ photo => 'gif'  ],   # an animated one becomes its first frame
  MOV  => [ video => 'mov'  ],
  MP4  => [ video => 'mp4'  ],
  M4V  => [ video => 'm4v'  ],
  AVI  => [ video => 'avi'  ],   # old cameras' clips; ffmpeg reads them fine
);

=method ingest_files

  my @photos = $ingest->ingest_files(@paths);

This ingests each file and returns the L<Jiggle::Photo> objects created.
Files already in the library (by digest) and files of unsupported types are
skipped, with a message sent to the logger.

=cut

sub ingest_files ($self, @paths) {
  return map {; $_->{status} eq 'ingested' ? $_->{photo} : () }
         map {; $self->ingest_file($_) } @paths;
}

=method ingest_file

  my $result = $ingest->ingest_file($path, \%arg);

This ingests one file, and returns a hash describing what happened:

  { status => 'ingested', id => $id, photo => $photo }
  { status => 'exists',   id => $id }    # already in the library
  { status => 'skipped',  reason => $why }

The optional arguments are for importers, which know more about a file than
its EXIF data does:

=for :list
* metadata
A code reference, called with the facts read from the file (as a hash
reference: C<kind> ("photo" or "video"), C<taken>, C<location>, C<width>,
C<rotation>, and so on).  It returns a hash
reference of L<Jiggle::Photo> attributes, which are used in place of the
defaults: C<title>, C<taken>, C<tags>, and so on.
* record_source_mtime
If false, C<source_mtime> isn't recorded.  Default: true.  An importer whose
files' mtimes mean nothing (like a download's) should turn this off.

=cut

sub ingest_file ($self, $path, $arg = {}) {
  $path = Path::Tiny::path($path);

  my $digest = Digest::SHA->new(256)->addfile("$path")->hexdigest;
  my $id     = $self->library->id_for_digest($digest);

  if (-e (my $meta = $self->library->meta_path($id))) {
    my $existing = Jiggle::Photo->from_toml_file($meta);

    die "id collision: $path and photo $id have different digests\n"
      unless $existing->sha256 eq $digest;

    $self->logger->("skip $path: already in library as $id");
    return { status => 'exists', id => $id };
  }

  my $facts = $self->_facts_for($path);

  my $media = $MEDIA_FOR_TYPE{ $facts->{type} // '' };
  unless ($media) {
    my $reason = 'unsupported type ' . ($facts->{type} // 'unknown');
    $self->logger->("skip $path: $reason");
    return { status => 'skipped', reason => $reason };
  }

  my ($kind, $ext) = @$media;

  # A Live Photo is a still plus a short clip, sharing a content identifier.
  # Until there's a policy for them, the clip is skipped rather than being
  # ingested as a video of its own.
  if ($facts->{live_photo}) {
    my $reason = 'Live Photo motion, not yet supported';
    $self->logger->("skip $path: $reason");
    return { status => 'skipped', reason => $reason };
  }

  # The callback also learns what kind of media this is.
  my $extra = $arg->{metadata} ? $arg->{metadata}->({ %$facts, kind => $kind }) : {};

  my $photo = Jiggle::Photo->new({
    type  => $kind,
    taken => $facts->{taken},
    added => _datetime_with_offset(time),    # an importer may say otherwise
    ($facts->{location} ? (location => $facts->{location}) : ()),
    %$extra,
    id    => $id,
    original => {
      file   => $path->basename,
      ext    => $ext,
      sha256 => $digest,
      bytes  => -s $path,
      (($arg->{record_source_mtime} // 1)
        ? (source_mtime => _datetime_with_offset($path->stat->mtime))
        : ()),
      width  => $facts->{width},
      height => $facts->{height},
      (defined $facts->{duration} ? (duration => 0 + $facts->{duration}) : ()),
    },
  });

  $self->_install_original($path, $photo);
  $self->library->add_photo($photo);

  $self->logger->("ingest $path as $id");
  return { status => 'ingested', id => $id, photo => $photo };
}

sub _install_original ($self, $source, $photo) {
  my $dest = $self->library->original_path($photo);
  $dest->parent->mkdir;

  # Copy to a temporary name and rename, so an interrupted ingest never
  # leaves a truncated file at a path that looks finished.
  my $tmp = $dest->sibling('.' . $dest->basename . '.tmp');

  # On macOS, cp -c clones the file when source and destination are on the
  # same APFS volume: instant, and taking no extra space until one of them
  # changes.  That turns importing a backup that's on the library's volume
  # from copying tens of gigabytes into nearly nothing.  Across volumes it
  # copies.  -- claude, 2026-09-27
  if ($^O eq 'darwin') {
    system('cp', '-c', "$source", "$tmp") == 0
      or die "can't copy $source to $tmp\n";
  } else {
    File::Copy::copy("$source", "$tmp") or die "can't copy $source to $tmp: $!";
  }

  chmod 0444, "$tmp";
  rename "$tmp", "$dest" or die "can't rename $tmp to $dest: $!";

  return;
}

# An epoch time as a TOML datetime in local time, with the UTC offset in
# effect at that moment: 2026-07-19T17:49:45-04:00.
sub _datetime_with_offset ($epoch) {
  my @t = localtime $epoch;
  my $datetime = POSIX::strftime('%Y-%m-%dT%H:%M:%S', @t);
  my $offset   = POSIX::strftime('%z', @t);
  $offset =~ s/\A([-+]\d\d)(\d\d)\z/$1:$2/;
  return "$datetime$offset";
}

sub _facts_for ($self, $path) {
  my $exif = Image::ExifTool->new;
  $exif->Options(PrintConv => 0);
  $exif->ExtractInfo("$path");

  my $tag = sub ($name) { $exif->GetValue($name) };

  my %facts = (type => $tag->('FileType'));

  my $media = $MEDIA_FOR_TYPE{ $facts{type} // '' };
  return _video_facts($tag, \%facts) if $media and $media->[0] eq 'video';

  my ($w, $h) = ($tag->('ImageWidth'), $tag->('ImageHeight'));
  ($w, $h) = ($h, $w) if ($tag->('Orientation') // 1) >= 5;

  # The clockwise turn the EXIF orientation calls for, which libvips applies.
  # Mirrored orientations (2, 4, 5, 7) are counted by their turn alone.
  $facts{rotation} = { 3 => 180, 4 => 180, 5 => 90, 6 => 90, 7 => 270, 8 => 270 }
    ->{ $tag->('Orientation') // 1 } // 0;
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

sub _video_facts ($tag, $facts) {
  $facts->{duration} = $tag->('Duration');

  # The stored frame size is before rotation; a portrait clip from a phone is
  # stored as landscape with a 90-degree rotation.
  my ($w, $h) = ($tag->('ImageWidth'), $tag->('ImageHeight'));
  ($w, $h) = ($h, $w) if ($tag->('Rotation') // 0) % 180;
  $facts->{rotation} = ($tag->('Rotation') // 0) % 360;
  @$facts{qw( width height )} = ($w, $h);

  # QuickTime's CreationDate is local time with an offset.  CreateDate is in
  # UTC with no marker, so if it's all we have, we say so with a Z.
  if (my $dt = $tag->('CreationDate')) {
    if (my ($y, $m, $d, $time, $offset) = $dt =~ /\A(\d{4}):(\d\d):(\d\d) (\d\d:\d\d:\d\d)(?:\.\d+)?([-+]\d\d:\d\d)?/) {
      $facts->{taken} = "$y-$m-${d}T$time" . ($offset // '');
    }
  } elsif (my $utc = $tag->('CreateDate')) {
    if (my ($y, $m, $d, $time) = $utc =~ /\A(\d{4}):(\d\d):(\d\d) (\d\d:\d\d:\d\d)/) {
      $facts->{taken} = "$y-$m-${d}T${time}Z" unless $y eq '0000';
    }
  } elsif (my $local = $tag->('DateTimeOriginal')) {
    # Old cameras' AVI files carry an EXIF-style local time instead.
    if (my ($y, $m, $d, $time) = $local =~ /\A(\d{4}):(\d\d):(\d\d) (\d\d:\d\d:\d\d)/) {
      $facts->{taken} = "$y-$m-${d}T$time";
    }
  }

  # With PrintConv off, GPSCoordinates is signed decimal "lat lon [alt]".
  if (my $coords = $tag->('GPSCoordinates')) {
    my ($lat, $lon) = split / /, $coords;
    $facts->{location} = { lat => 0 + $lat, lon => 0 + $lon }
      if defined $lon;
  }

  $facts->{live_photo} = 1 if defined $tag->('ContentIdentifier');

  return $facts;
}

1;
