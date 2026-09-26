package Jiggle::Library;
use v5.36;

use Moo;

use Jiggle::Album;
use Jiggle::Photo;
use Path::Tiny ();
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

has _photo_index => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my %photo;

    return \%photo unless -d $self->meta_dir;

    for my $shard ($self->meta_dir->children) {
      next unless $shard->is_dir;
      next if $shard->basename eq 'albums';

      for my $file ($shard->children(qr/\.toml\z/)) {
        my $photo = Jiggle::Photo->from_toml_file($file);
        $photo{ $photo->id } = $photo;
      }
    }

    return \%photo;
  },
);

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

This writes a new photo's metadata file and adds it to the in-memory index.
It dies if a photo with that id already has a metadata file.

=cut

sub add_photo ($self, $photo) {
  my $path = $self->meta_path($photo->id);
  die "refusing to overwrite $path\n" if -e $path;

  $path->parent->mkdir;
  $path->spew_utf8($photo->as_toml);

  $self->_photo_index->{ $photo->id } = $photo;
  return;
}

1;
