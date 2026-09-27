package Jiggle::Site::Writer;
use v5.36;

use Moo;

use Path::Tiny ();

=head1 NAME

Jiggle::Site::Writer - put files into the output tree, touching only what changed

=head1 DESCRIPTION

The site is rebuilt in full every time, but the sync to the web host compares
files, so the build must not disturb files whose content hasn't changed.  The
writer handles that: content is written only when it differs from what's on
disk, and renditions are hardlinked from the derived tree rather than copied.

The writer also remembers every path it was asked to produce.  After a build,
C<prune> removes everything else, which is how a photo that became private
(or was deleted) disappears from the published site.

=cut

has root => (
  is => 'ro',
  required => 1,
  coerce   => sub ($r) { Path::Tiny::path($r)->absolute },
);

has _produced => (is => 'ro', init_arg => undef, default => sub { {} });

has stats => (
  is => 'ro',
  init_arg => undef,
  default  => sub { { written => 0, unchanged => 0, linked => 0, pruned => 0 } },
);

sub _claim ($self, $rel) {
  die "path $rel produced twice\n" if $self->_produced->{$rel}++;
  return $self->root->child($rel);
}

=method write_file

  $writer->write_file($relative_path, $content);

C<$content> is bytes.  The file is written only if it doesn't exist or its
contents differ.

=cut

sub write_file ($self, $rel, $content) {
  my $dest = $self->_claim($rel);

  if (-e $dest and -s _ == length $content and $dest->slurp_raw eq $content) {
    $self->stats->{unchanged}++;
    return;
  }

  $dest->parent->mkpath;
  $dest->spew_raw($content);
  $self->stats->{written}++;
  return;
}

=method link_file

  $writer->link_file($relative_path, $source);

This makes the destination a hardlink to C<$source>, unless it already is one.
If hardlinking fails (say, because the output is on another filesystem), the
file is copied instead.

=cut

sub link_file ($self, $rel, $source) {
  my $dest = $self->_claim($rel);

  my @src = stat $source or die "can't stat $source: $!";
  my @dst = stat $dest;

  # Same device and inode means it's already the right link.
  return if @dst and $src[0] == $dst[0] and $src[1] == $dst[1];

  $dest->parent->mkpath;
  unlink $dest if @dst;

  unless (link "$source", "$dest") {
    Path::Tiny::path($source)->copy($dest);
  }

  $self->stats->{linked}++;
  return;
}

=method prune

  $writer->prune;
  $writer->prune({ except => 'pagefind/' });
  $writer->prune({ only   => 'pagefind/' });

This removes every file under the root that wasn't produced during this run,
then removes any directories left empty.  C<except> and C<only> take a path
prefix, and limit pruning to files outside or inside it.  That lets a build
prune in two passes, around a step that reads the output tree.

=cut

sub prune ($self, $arg = {}) {
  my @dirs;

  my $iter = $self->root->iterator({ recurse => 1 });
  while (my $path = $iter->()) {
    if ($path->is_dir) {
      push @dirs, $path;
      next;
    }

    my $rel = $path->relative($self->root)->stringify;
    next if $self->_produced->{$rel};

    next if defined $arg->{except} and index($rel, $arg->{except}) == 0;
    next if defined $arg->{only}   and index($rel, $arg->{only})   != 0;

    $path->remove;
    $self->stats->{pruned}++;
  }

  # Deepest first, so a parent is examined after its children are gone.
  for my $dir (sort { length $b <=> length $a } @dirs) {
    rmdir $dir;  # fails harmlessly unless empty
  }

  return;
}

1;
