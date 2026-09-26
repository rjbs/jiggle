package Jiggle::Derive;
use v5.36;

use Moo;

use JSON::MaybeXS ();
use List::Util ();
use Parallel::ForkManager;

=head1 NAME

Jiggle::Derive - make the published renditions of each photo

=head1 SYNOPSIS

  my $derive = Jiggle::Derive->new({ library => $library });
  $derive->derive_photos($library->photos);

=head1 DESCRIPTION

Every photo gets a fixed set of renditions, made with libvips.  Each is
rotated upright, converted to sRGB, and stripped of all metadata (which is
what keeps EXIF GPS out of published files).

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

# Each recipe is: the rendition's filename, a version, and how to make it.
# "box" fits the image inside an NxN square, never enlarging it; "square"
# crops to an NxN square, choosing the crop by libvips's attention heuristic.
my @RECIPES = (
  { name => 'sq300.webp', version => 1, square => 300,  opts => 'Q=75' },
  { name => '500.webp',   version => 1, box    => 500,  opts => 'Q=80' },
  { name => '1024.webp',  version => 1, box    => 1024, opts => 'Q=80' },
  { name => '2048.webp',  version => 1, box    => 2048, opts => 'Q=80' },
  { name => 'og.jpg',     version => 1, box    => 1200, opts => 'Q=85' },
);

sub recipes ($class) { @RECIPES }

=method rendition_size

  my ($w, $h) = Jiggle::Derive->rendition_size($photo, $name);

This returns the pixel dimensions a rendition has (or will have), computed
from the original's dimensions, without looking at any file.

=cut

sub rendition_size ($class, $photo, $name) {
  my ($recipe) = grep {; $_->{name} eq $name } @RECIPES;
  die "unknown rendition $name" unless $recipe;

  return ($recipe->{square}) x 2 if $recipe->{square};

  my ($w, $h) = ($photo->width, $photo->height);
  my $scale = List::Util::min(1, $recipe->{box} / List::Util::max($w, $h));

  # libvips rounds to nearest when it shrinks, so we do too.
  return (int($w * $scale + 0.5), int($h * $scale + 0.5));
}

=method derive_photos

  $derive->derive_photos(@photos);

This makes any missing or stale renditions for the given photos, working on
several photos at once.  It returns the number of renditions made.

=cut

my $JSON = JSON::MaybeXS->new->canonical->pretty;

sub derive_photos ($self, @photos) {
  my @work = grep {; $self->_stale_recipes($_) } @photos;
  return 0 unless @work;

  $self->logger->(sprintf "deriving renditions for %d photo(s)", 0 + @work);

  my $made = 0;
  my @failed;

  my $pm = Parallel::ForkManager->new($self->jobs);
  $pm->run_on_finish(sub ($pid, $exit, $id, $signal, $core, $data) {
    if ($exit or $signal) {
      push @failed, $id;
    } else {
      $made += $data->{made};
    }
  });

  for my $photo (@work) {
    $pm->start($photo->id) and next;

    my $n = eval { $self->_derive_one($photo) };
    unless (defined $n) {
      warn "error deriving " . $photo->id . ": $@";
      $pm->finish(1);
    }

    $pm->finish(0, { made => $n });
  }

  $pm->wait_all_children;

  die "failed to derive: @failed\n" if @failed;
  return $made;
}

sub _state_file ($self, $photo) {
  $self->library->derived_path($photo->id, 'state.json');
}

sub _state ($self, $photo) {
  my $file = $self->_state_file($photo);
  return {} unless -e $file;
  return $JSON->decode($file->slurp_raw);
}

sub _stale_recipes ($self, $photo) {
  my $state = $self->_state($photo);
  my $dir   = $self->library->derived_path($photo->id);

  return grep {;
    my $have = $state->{ $_->{name} };
       ! $have
    || $have->{sha256}  ne $photo->sha256
    || $have->{version} != $_->{version}
    || ! -e $dir->child($_->{name})
  } @RECIPES;
}

sub _derive_one ($self, $photo) {
  my $source = $self->library->original_path($photo);
  my $dir    = $self->library->derived_path($photo->id);
  $dir->mkpath;

  my $state = $self->_state($photo);
  my $made  = 0;

  for my $recipe ($self->_stale_recipes($photo)) {
    my $dest = $dir->child($recipe->{name});

    # Write to a temporary name (keeping the extension, which is how libvips
    # picks the output format) and rename, so a killed build never leaves a
    # partial file where a finished one belongs.
    my $tmp = $dir->child(".tmp-$recipe->{name}");

    my @size = $recipe->{square}
             ? ($recipe->{square}, '--height', $recipe->{square}, '--crop', 'attention')
             : ($recipe->{box},    '--height', $recipe->{box});

    my @cmd = (
      'vips', 'thumbnail', "$source", "$tmp\[$recipe->{opts},keep=none]",
      @size,
      '--size', 'down',
      '--export-profile', 'srgb',
    );

    system(@cmd) == 0 or die "command failed: @cmd\n";
    rename "$tmp", "$dest" or die "can't rename $tmp to $dest: $!";

    $state->{ $recipe->{name} } = {
      sha256  => $photo->sha256,
      version => $recipe->{version},
    };

    $made++;
  }

  $self->_state_file($photo)->spew_raw($JSON->encode($state));
  return $made;
}

1;
