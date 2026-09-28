package Jiggle::Derive;
use v5.36;

use Moo;

use JSON::MaybeXS ();
use List::Util ();
use Jiggle::Progress;
use Parallel::ForkManager;
use Path::Tiny ();

=head1 NAME

Jiggle::Derive - make the published renditions of each photo

=head1 SYNOPSIS

  my $derive = Jiggle::Derive->new({ library => $library });
  $derive->derive_photos($library->photos);

=head1 DESCRIPTION

Every photo gets a fixed set of image renditions, made with libvips.  Each is
rotated upright, converted to sRGB, and stripped of all metadata (which is
what keeps EXIF GPS out of published files).

A video gets the same image renditions, made from a poster frame, plus an MP4
for the web made with ffmpeg: H.264 and AAC, rotated upright, with only the
first video and audio streams, no metadata (so no GPS), and its index at the
front so playback can start before the download finishes.  The poster frame
itself (F<poster.png>) is an intermediate, and isn't published.

Each photo's derived directory holds a F<state.json> recording, for each
rendition, the digest of the original and the recipe version that produced
it.  A rendition is remade only when one of those has changed, so bumping a
recipe's version remakes that rendition across the library and nothing else.

=cut

has library => (is => 'ro', required => 1);
has jobs    => (is => 'ro', default  => sub { _cpu_count() });
has logger  => (is => 'ro', default  => sub { sub { } });

sub _cpu_count {
  chomp(my $n = `sysctl -n hw.ncpu 2>/dev/null` || `nproc 2>/dev/null` || 4);
  return $n;
}

# Each recipe is: the rendition's filename, a version, what kind of thing it
# is, and how to make it.  "fit" shrinks to fit inside a WxH box, never
# enlarging.  "for" limits a recipe to one type of media; without it, a recipe
# applies to everything.  "publish" is false for intermediates.
#
# h480 is for the justified grids, where every photo in a row has the same
# height.  The width limit only matters for panoramas.
#
# Order matters: a video's poster must be made before the images made from it.
my @RECIPES = (
  { name => 'poster.png', version => 1, kind => 'poster', for => 'video', publish => 0 },
  { name => 'h480.webp',  version => 1, kind => 'image', fit => [ 1920,  480 ], opts => 'Q=75' },
  { name => '500.webp',   version => 1, kind => 'image', fit => [  500,  500 ], opts => 'Q=80' },
  { name => '1024.webp',  version => 1, kind => 'image', fit => [ 1024, 1024 ], opts => 'Q=80' },
  { name => '2048.webp',  version => 1, kind => 'image', fit => [ 2048, 2048 ], opts => 'Q=80' },
  { name => 'og.jpg',     version => 1, kind => 'image', fit => [ 1200, 1200 ], opts => 'Q=85' },
  { name => 'video.mp4',  version => 1, kind => 'video', for => 'video', fit => [ 1920, 1920 ] },
);

my ($POSTER) = grep {; $_->{kind} eq 'poster' } @RECIPES;

sub recipes ($class) { @RECIPES }

=method recipes_for

  my @recipes = Jiggle::Derive->recipes_for($photo);

This returns the recipes that apply to the given photo (or video), in the
order they must be made.

=method published_recipes_for

This is like C<recipes_for>, but leaves out intermediates.

=cut

sub recipes_for ($class, $photo) {
  grep {; ($_->{for} // $photo->type) eq $photo->type } @RECIPES;
}

sub published_recipes_for ($class, $photo) {
  grep {; $_->{publish} // 1 } $class->recipes_for($photo);
}

=method rendition_size

  my ($w, $h) = Jiggle::Derive->rendition_size($photo, $name);

This returns the pixel dimensions a rendition has (or will have), computed
from the original's dimensions, without looking at any file.

=cut

sub rendition_size ($class, $photo, $name) {
  my ($recipe) = grep {; $_->{name} eq $name } @RECIPES;
  die "unknown rendition $name" unless $recipe;

  my ($w, $h) = ($photo->width, $photo->height);
  return ($w, $h) unless $recipe->{fit};

  my ($max_w, $max_h) = $recipe->{fit}->@*;
  my $scale = List::Util::min(1, $max_w / $w, $max_h / $h);

  # libvips rounds to nearest when it shrinks, so we do too.  (For video,
  # ffmpeg is told to round to an even number; see _make_video.)
  return (int($w * $scale + 0.5), int($h * $scale + 0.5))
    unless $recipe->{kind} eq 'video';

  return map {; 2 * int($_ * $scale / 2 + 0.5) } ($w, $h);
}

=method derive_photos

  $derive->derive_photos(@photos);

This makes any missing or stale renditions for the given photos, working on
several photos at once.  It returns the number of renditions made.

=cut

my $JSON = JSON::MaybeXS->new->canonical->pretty;

sub derive_photos ($self, @photos) {
  $self->remove_obsolete(@photos);

  my @work = grep {; $self->_stale_recipes($_) } @photos;
  return 0 unless @work;

  $self->logger->(sprintf "deriving renditions for %d photo(s)", 0 + @work);

  my $progress = Jiggle::Progress->new({
    label  => 'renditions',
    total  => scalar @work,
    logger => $self->logger,
  });

  my $made = 0;
  my @failed;

  my $pm = Parallel::ForkManager->new($self->jobs);
  $pm->run_on_finish(sub ($pid, $exit, $id, $signal, $core, $data) {
    if ($exit or $signal) {
      push @failed, $id;
    } else {
      $made += $data->{made};
    }

    if ($data and $data->{warnings} and $data->{warnings}->@*) {
      $self->logger->("warning: $id: $_") for $data->{warnings}->@*;
      $self->_warned->{$id} = $data->{warnings};
    }

    $progress->tick;
  });

  for my $photo (@work) {
    $pm->start($photo->id) and next;

    # libvips and ffmpeg report damaged originals (a truncated JPEG, say) as
    # warnings on stderr, and carry on.  Each photo's are collected here, so
    # they can be reported by photo instead of lost in interleaved output.
    # -- claude, 2026-09-27
    my $errors = Path::Tiny->tempfile;
    open STDERR, '>', "$errors" or die "can't redirect stderr: $!";

    my $n = eval { $self->_derive_one($photo) };
    my $error = $@;
    my @warnings = _warnings_from($errors);

    unless (defined $n) {
      $pm->finish(1, { warnings => [ @warnings, "error: $error" ] });
    }

    $pm->finish(0, { made => $n, warnings => \@warnings });
  }

  $pm->wait_all_children;
  $progress->done;

  my $warned = keys $self->_warned->%*;
  $self->logger->("$warned photo(s) had warnings; see above") if $warned;

  # One unreadable original (a truncated download, say) mustn't stop a build
  # of thousands, so failures are reported, not fatal.  The site leaves out
  # any photo whose renditions are missing.  -- claude, 2026-09-27
  if (@failed) {
    $self->_failed->@* = @failed;
    $self->logger->(sprintf 'failed to make renditions for %d photo(s): %s',
      0 + @failed, join q{ }, sort @failed);
  }

  return $made;
}

=method failed

This returns the ids of the photos whose renditions couldn't be made in the
last C<derive_photos>.

=cut

has _failed => (is => 'ro', init_arg => undef, default => sub { [] });

sub failed ($self) { $self->_failed->@* }

=method warnings

This returns a hash of the warnings from the last C<derive_photos>, keyed by
photo id.

=cut

has _warned => (is => 'ro', init_arg => undef, default => sub { {} });

sub warnings ($self) { $self->_warned->%* }

# The distinct messages in a stderr capture, without libvips's prefix of
# program, pid, and time.
sub _warnings_from ($file) {
  my %seen;
  return grep {; length && ! $seen{$_}++ }
         map  {; s/\A\(\S+:\d+\): (?:VIPS-)?WARNING \*\*: [\d:.]+: //r =~ s/\s+\z//r }
         $file->lines_utf8;
}

=method remove_obsolete

  $derive->remove_obsolete(@photos);

This deletes files in each photo's derived directory that no current recipe
produces: renditions whose recipe was removed or renamed, and temporary files
left by an interrupted run.  It returns the number of files removed.

=cut

sub remove_obsolete ($self, @photos) {
  my $removed = 0;

  for my $photo (@photos) {
    my $dir = $self->library->derived_path($photo->id);
    next unless -d $dir;

    my %keep = map {; $_->{name} => 1 } $self->recipes_for($photo);
    $keep{'state.json'} = 1;

    my @obsolete = grep {; ! $keep{ $_->basename } } $dir->children;
    next unless @obsolete;

    $_->remove for @obsolete;
    $removed += @obsolete;

    my $state = $self->_state($photo);
    delete @$state{ grep {; ! $keep{$_} } keys %$state };
    $self->_state_file($photo)->spew_raw($JSON->encode($state));
  }

  $self->logger->("removed $removed obsolete rendition file(s)") if $removed;
  return $removed;
}

sub _state_file ($self, $photo) {
  $self->library->derived_path($photo->id, 'state.json');
}

sub _state ($self, $photo) {
  my $file = $self->_state_file($photo);
  return {} unless -e $file;
  return $JSON->decode($file->slurp_raw);
}

# A video's images are made from its poster, so they're stale when the poster
# recipe changes, too.  Folding the poster's version into theirs handles that
# without any special cases elsewhere.  -- claude, 2026-09-27
sub _version_of ($self, $photo, $recipe) {
  my $version = "$recipe->{version}";

  $version .= "+poster$POSTER->{version}"
    if $photo->type eq 'video' and $recipe->{kind} eq 'image';

  # An extra rotation changes every rendition, so it's part of the version:
  # changing a photo's rotate remakes its renditions, and only its.
  $version .= "+rot" . $photo->rotate if $photo->rotate;

  return $version;
}

sub _stale_recipes ($self, $photo) {
  my $state = $self->_state($photo);
  my $dir   = $self->library->derived_path($photo->id);

  return grep {;
    my $have = $state->{ $_->{name} };
       ! $have
    || $have->{sha256}  ne $photo->sha256
    || $have->{version} ne $self->_version_of($photo, $_)
    || ! -e $dir->child($_->{name})
  } $self->recipes_for($photo);
}

sub _derive_one ($self, $photo) {
  my $original = $self->library->original_path($photo);
  my $dir      = $self->library->derived_path($photo->id);
  $dir->mkpath;

  my $state = $self->_state($photo);
  my $made  = 0;

  my $image_source = $photo->type eq 'video' ? $dir->child($POSTER->{name})
                   :                           $original;

  for my $recipe ($self->_stale_recipes($photo)) {
    my $dest = $dir->child($recipe->{name});

    # Write to a temporary name (keeping the extension, which is how libvips
    # and ffmpeg pick the output format) and rename, so a killed build never
    # leaves a partial file where a finished one belongs.
    my $tmp = $dir->child(".tmp-$recipe->{name}");

    # A video's poster and video are turned as they're made, so the images
    # made from its poster are upright already.
    my $turn = $photo->type eq 'video' && $recipe->{kind} eq 'image' ? 0 : $photo->rotate;

    if    ($recipe->{kind} eq 'image')  { $self->_make_image($image_source, $tmp, $recipe, $turn) }
    elsif ($recipe->{kind} eq 'poster') { $self->_make_poster($photo, $original, $tmp) }
    elsif ($recipe->{kind} eq 'video')  { $self->_make_video($original, $tmp, $recipe, $photo->rotate) }
    else  { die "unknown recipe kind $recipe->{kind}\n" }

    rename "$tmp", "$dest" or die "can't rename $tmp to $dest: $!";

    $state->{ $recipe->{name} } = {
      sha256  => $photo->sha256,
      version => $self->_version_of($photo, $recipe),
    };

    $made++;
  }

  $self->_state_file($photo)->spew_raw($JSON->encode($state));
  return $made;
}

sub _run (@cmd) {
  system(@cmd) == 0 or die "command failed: @cmd\n";
}

sub _make_image ($self, $source, $dest, $recipe, $rotate = 0) {
  my ($w, $h) = $recipe->{fit}->@*;
  my $out = "$dest\[$recipe->{opts},keep=none]";

  unless ($rotate) {
    _run(
      'vips', 'thumbnail', "$source", $out, $w, '--height', $h,
      '--size', 'down', '--export-profile', 'srgb',
    );
    return;
  }

  # With an extra rotation, shrink into the box as it will be after turning
  # (swapped for a quarter turn), into an uncompressed intermediate, then
  # turn it while encoding the rendition.
  ($w, $h) = ($h, $w) if $rotate % 180;
  my $tmp = Path::Tiny->tempfile(SUFFIX => '.v');

  _run(
    'vips', 'thumbnail', "$source", "$tmp", $w, '--height', $h,
    '--size', 'down', '--export-profile', 'srgb',
  );
  _run('vips', 'rot', "$tmp", $out, "d$rotate");
}

my @FFMPEG = qw( ffmpeg -nostdin -hide_banner -loglevel error -y );

sub _make_poster ($self, $photo, $source, $dest) {
  # A frame a moment in is more representative than the very first one,
  # which is often blurry or dark.  For very short clips, take the middle.
  my $at = List::Util::min(1, ($photo->duration // 0) / 2);

  # ffmpeg applies the rotation flag while decoding, so the frame comes out
  # upright, which is what the image renditions expect.  Any extra rotation
  # is applied here, too.
  my @turn = _turn_filter($photo->rotate);

  _run(
    @FFMPEG,
    '-ss', $at, '-i', "$source",
    (@turn ? ('-vf', join q{,}, @turn) : ()),
    '-frames:v', 1,
    '-update', 1,
    "$dest",
  );
}

# ffmpeg filters that turn a frame clockwise by the given number of degrees.
sub _turn_filter ($rotate) {
  return ()                                 unless $rotate;
  return ('transpose=clock')                if $rotate == 90;
  return ('hflip', 'vflip')                 if $rotate == 180;
  return ('transpose=cclock')               if $rotate == 270;
  die "can't turn by $rotate degrees\n";
}

sub _make_video ($self, $source, $dest, $recipe, $rotate = 0) {
  my ($max_w, $max_h) = $recipe->{fit}->@*;
  my @turn = _turn_filter($rotate);

  _run(
    @FFMPEG,
    '-i', "$source",

    # Only the first video stream and the first audio stream, if any.  iPhone
    # files also carry spatial audio and timed metadata tracks, which
    # browsers can't use and which could carry location.
    '-map', '0:v:0', '-map', '0:a:0?',

    # Scaling happens after rotation, so these limits apply to the upright
    # frame.  H.264 needs even dimensions.
    '-vf', join(q{,}, @turn,
      "scale=w='min($max_w,iw)':h='min($max_h,ih)'"
      . ':force_original_aspect_ratio=decrease:force_divisible_by=2'),

    '-c:v', 'libx264', '-preset', 'slow', '-crf', 23,
    '-profile:v', 'high', '-pix_fmt', 'yuv420p',
    '-c:a', 'aac', '-b:a', '128k', '-ac', 2,

    '-map_metadata', -1, '-map_chapters', -1,
    '-movflags', '+faststart',
    "$dest",
  );
}

1;
