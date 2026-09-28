package Jiggle::Library;
use v5.36;

use Moo;

use Jiggle::Album;
use Jiggle::Photo;
use JSON::MaybeXS ();
use Jiggle::Progress;
use Path::Tiny ();
use Time::HiRes ();
use Jiggle::TOML qw( load_toml_file );

=head1 NAME

Jiggle::Library - a directory of originals, metadata, and derivatives

=head1 SYNOPSIS

  my $library = Jiggle::Library->new({ root => $dir });

  for my $photo ($library->photos) {
    say $library->original_path($photo);
  }

=head1 DESCRIPTION

A library is a directory with a F<jiggle.toml> at its root, and three trees
beneath it.  Every file belonging to a photo is found by computing its path
from the photo's id; nothing needs to be looked up.  All of that computation
happens here, so the sharding rule can change without touching callers.

=cut

has root => (
  is  => 'ro',
  required => 1,
  coerce   => sub ($r) { Path::Tiny::path($r)->absolute },
);

has logger => (is => 'ro', default => sub { sub { } });

has config => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my $file = $self->root->child('jiggle.toml');
    return {} unless -e $file;
    return scalar load_toml_file($file);
  },
);

sub originals_dir ($self) { $self->root->child('originals') }
sub meta_dir      ($self) { $self->root->child('meta')      }
sub derived_dir   ($self) { $self->root->child('derived')   }
sub albums_dir    ($self) { $self->meta_dir->child('albums') }

=method id_for_digest

  my $id = $library->id_for_digest($sha256_hex);

A photo's id is the first 12 hex digits of its original's SHA-256, always.
Replacing an original's bytes makes a new photo with a new id.

=cut

sub id_for_digest ($self, $digest) {
  die "not a SHA-256 hex digest: $digest\n" unless $digest =~ /\A[0-9a-f]{64}\z/;
  return substr $digest, 0, 12;
}

sub shard_for ($self, $id) {
  return substr $id, 0, 2;
}

sub meta_path ($self, $id) {
  $self->meta_dir->child($self->shard_for($id), "$id.toml");
}

sub original_path ($self, $photo) {
  my $id = $photo->id;
  $self->originals_dir->child($self->shard_for($id), "$id." . $photo->ext);
}

sub derived_path ($self, $id, $rendition = undef) {
  my $dir = $self->derived_dir->child($self->shard_for($id), $id);
  return defined $rendition ? $dir->child($rendition) : $dir;
}

=method photos

This returns every photo in the library, public or private, in no particular
order.  The metadata tree is read once and cached.

=cut

my $CACHE_JSON = JSON::MaybeXS->new->canonical->utf8;

# How many metadata files the last load had to parse, for testing the cache.
has _reparsed => (is => 'rw', init_arg => undef);

sub reparsed ($self) { $self->_photo_index; $self->_reparsed }

has _photo_index => (
  is => 'lazy',
  init_arg  => undef,
  predicate => '_has_photo_index',
  default  => sub ($self) {
    my %photo;

    return \%photo unless -d $self->meta_dir;

    # Parsing thousands of TOML files is slow (and reading them is slower,
    # on slow storage), so the parsed data is cached, and reused for any file
    # whose size and modification time are what they were.  That costs one
    # stat per file.  -- claude, 2026-09-28
    my $cache_file = $self->state_dir->child('meta-cache.json');
    my $old = eval { $CACHE_JSON->decode($cache_file->slurp_raw)->{files} } // {};
    my (%new, $misses);

    my $progress = Jiggle::Progress->new({
      label  => 'reading metadata',
      total  => scalar keys %$old || undef,
      logger => $self->logger,
    });

    for my $shard ($self->meta_dir->children) {
      next unless $shard->is_dir;
      next if $shard->basename eq 'albums';

      for my $file ($shard->children(qr/\.toml\z/)) {
        $progress->tick;
        my (undef, undef, undef, undef, undef, undef, undef, $size, undef, $mtime)
          = Time::HiRes::stat("$file");

        # As a string with fixed precision: a float's round trip through
        # JSON loses digits, and then no cached time would ever match.
        $mtime = sprintf '%.6f', $mtime;

        my $rel = $file->relative($self->meta_dir)->stringify;
        my $had = $old->{$rel};

        my $data;
        if ($had and $had->{size} == $size and $had->{mtime} eq $mtime) {
          $data = $had->{data};
        } else {
          $data = load_toml_file($file);
          $misses++;
        }

        $new{$rel} = { size => $size, mtime => $mtime, data => $data };

        my $photo = eval { Jiggle::Photo->new({ %$data }) };
        die "error loading $file: $@" unless $photo;
        $photo{ $photo->id } = $photo;
      }
    }

    $progress->done;
    $self->logger->(sprintf 'metadata: %d photo(s), %d file(s) reparsed',
      scalar keys %photo, $misses // 0);
    $self->_reparsed($misses // 0);

    if ($misses or keys %$old != keys %new) {
      $cache_file->parent->mkpath;
      $cache_file->spew_raw($CACHE_JSON->encode({ version => 1, files => \%new }));
    }

    return \%photo;
  },
);


=method state_dir

This returns the directory where jiggle keeps its own bookkeeping for the
library, like caches and manifests: F<.jiggle>, at the library's root.
Everything in it can be deleted, at the cost of a slower next build.

=cut

sub state_dir ($self) { $self->root->child('.jiggle') }

sub photos ($self) { values $self->_photo_index->%* }

sub photo ($self, $id) { $self->_photo_index->{$id} }

has _albums => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    return [] unless -d $self->albums_dir;
    return [
      map {; Jiggle::Album->from_toml_file($_) }
      sort $self->albums_dir->children(qr/\.toml\z/)
    ];
  },
);

sub albums ($self) { $self->_albums->@* }

=method add_photo

  $library->add_photo($photo);

This writes a new photo's metadata file, and adds it to the in-memory index if
that has been loaded.
It dies if a photo with that id already has a metadata file.

=cut

sub add_photo ($self, $photo) {
  my $path = $self->meta_path($photo->id);
  die "refusing to overwrite $path\n" if -e $path;

  $path->parent->mkdir;
  $path->spew_utf8($photo->as_toml);

  # Adding a photo mustn't force the whole metadata tree to be read.
  $self->_photo_index->{ $photo->id } = $photo if $self->_has_photo_index;
  return;
}

1;
