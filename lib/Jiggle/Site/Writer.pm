package Jiggle::Site::Writer;
use v5.36;

use Moo;

use Digest::SHA ();
use JSON::MaybeXS ();
use Path::Tiny ();

=head1 NAME

Jiggle::Site::Writer - put files into the output tree, touching only what changed

=head1 DESCRIPTION

The site is rebuilt in full every time, but the sync to the web host compares
files, so the build must not disturb files whose content hasn't changed.  The
writer handles that: content is written only when it differs from what's
there, and renditions are hardlinked from the derived tree rather than
copied.

The writer also remembers every path it was asked to produce.  After a build,
C<prune> removes everything else, which is how a photo that became private
(or was deleted) disappears from the published site.

=head2 The manifest

If given a C<manifest> file, the writer records there what it put at each
path: a digest of written content, or the source and key of a link.  The
next build then compares against the manifest instead of the files:
unchanged content isn't read back, unchanged links aren't stat'ed, and
pruning deletes the manifest's leftover paths without walking the tree.  On
slow storage, that's most of the cost of a build in which little changed.

The manifest is trusted, so a file changed by hand in the output isn't
noticed.  With C<verify>, or with no readable manifest, every file is
checked against the disk instead, and the manifest is rebuilt.

=cut

has root => (
  is => 'ro',
  required => 1,
  coerce   => sub ($r) { Path::Tiny::path($r)->absolute },
);

has manifest => (
  is => 'ro',
  coerce => sub ($m) { defined $m ? Path::Tiny::path($m) : undef },
);

has verify => (is => 'ro', default => 0);

my $JSON = JSON::MaybeXS->new->canonical->utf8;

# What the last build recorded, or undef if there's nothing to trust.
#
# A build that stops partway (killed, or dying on an error) may have written
# files its manifest never records.  If one of those is for a photo that's
# since been made private, a manifest-based prune would never remove it.  So
# a marker file says a build is under way, and is removed only when the
# manifest is saved; finding it means the last build didn't finish, and the
# manifest isn't trusted.  -- claude, 2026-09-28
has _old => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my $file = $self->manifest or return undef;

    my $marker = $self->_marker;
    my $interrupted = -e $marker;
    $marker->parent->mkpath;
    $marker->touch;

    return undef if $self->verify or $interrupted;
    return undef unless -e $file;

    my $data = eval { $JSON->decode($file->slurp_raw) };
    return $data && ref $data->{files} eq 'HASH' ? $data->{files} : undef;
  },
);

sub _marker ($self) { Path::Tiny::path($self->manifest . '.building') }

=method trusting_manifest

This is true if the writer is comparing against a manifest, and false if
it's checking the disk (because of C<verify>, or because there's no
manifest, or because the last build didn't finish).

=cut

sub trusting_manifest ($self) { defined $self->_old }

# What this build produced: relative path => manifest record.
has _produced => (is => 'ro', init_arg => undef, default => sub { {} });

has stats => (
  is => 'ro',
  init_arg => undef,
  default  => sub { { written => 0, unchanged => 0, linked => 0, pruned => 0 } },
);

# How many HTML files were written or removed, which tells whether the
# search index could need rebuilding.
has html_changes => (is => 'rw', init_arg => undef, default => 0);

sub _claim ($self, $rel, $record) {
  die "path $rel produced twice\n" if $self->_produced->{$rel};
  $self->_produced->{$rel} = $record;
  return $self->root->child($rel);
}

sub _changed ($self, $rel) {
  $self->html_changes($self->html_changes + 1) if $rel =~ /\.html\z/;
}

=method write_file

  $writer->write_file($relative_path, $content);

C<$content> is bytes.  The file is written only if its contents differ from
what's there.

=cut

sub write_file ($self, $rel, $content) {
  my $digest = Digest::SHA::sha1_hex($content);
  my $dest   = $self->_claim($rel, { sha1 => $digest });

  my $old = $self->_old;
  if ($old) {
    if (($old->{$rel}{sha1} // '') eq $digest) {
      $self->stats->{unchanged}++;
      return;
    }
  } elsif (-e $dest and -s _ == length $content and $dest->slurp_raw eq $content) {
    $self->stats->{unchanged}++;
    return;
  }

  $dest->parent->mkpath;
  $dest->spew_raw($content);
  $self->stats->{written}++;
  $self->_changed($rel);
  return;
}

=method link_file

  $writer->link_file($relative_path, $source, $key);

This makes the destination a hardlink to C<$source>, unless it already is
one.  C<$key> should change whenever C<$source> is replaced (it's the
rendition's version, for a rendition), and the link is remade when it does:
a replaced file is a new file, and an old hardlink would go on showing the
old one.  If hardlinking fails (say, because the output is on another
filesystem), the file is copied instead.

=cut

sub link_file ($self, $rel, $source, $key = '') {
  my $dest = $self->_claim($rel, { link => "$source", key => $key });

  if (my $old = $self->_old) {
    my $had = $old->{$rel};
    if ($had and ($had->{link} // '') eq "$source" and ($had->{key} // '') eq $key) {
      $self->stats->{unchanged}++;
      return;
    }
  }

  my @src = stat $source or die "can't stat $source: $!";
  my @dst = stat $dest;

  # Same device and inode means it's already the right link.
  if (@dst and $src[0] == $dst[0] and $src[1] == $dst[1]) {
    $self->stats->{unchanged}++;
    return;
  }

  $dest->parent->mkpath;
  unlink $dest if @dst;

  unless (link "$source", "$dest") {
    Path::Tiny::path($source)->copy($dest);
  }

  $self->stats->{linked}++;
  return;
}

=method keep

  $writer->keep('pagefind/');

This claims every path under the prefix that the last build produced, as if
it had been produced again, leaving the files alone.  It's for output that
a build can tell needn't be remade, like a search index when no page
changed.  It returns the number of paths kept, which is zero if there's no
manifest to go by.

=cut

sub keep ($self, $prefix) {
  my $old = $self->_old or return 0;

  my $n = 0;
  for my $rel (grep {; index($_, $prefix) == 0 } keys %$old) {
    next if $self->_produced->{$rel};
    $self->_produced->{$rel} = $old->{$rel};
    $self->stats->{unchanged}++;
    $n++;
  }

  return $n;
}

=method prune

  $writer->prune;
  $writer->prune({ except => 'pagefind/' });
  $writer->prune({ only   => 'pagefind/' });

This removes every file that wasn't produced during this run, then removes
any directories left empty.  C<except> and C<only> take a path prefix, and
limit pruning to files outside or inside it.  That lets a build prune in two
passes, around a step that reads the output tree.

With a manifest, the files to remove are the ones it lists that weren't
produced.  Without one, the whole tree is walked.

=cut

sub prune ($self, $arg = {}) {
  my $wanted = sub ($rel) {
    return 0 if $self->_produced->{$rel};
    return 0 if defined $arg->{except} and index($rel, $arg->{except}) == 0;
    return 0 if defined $arg->{only}   and index($rel, $arg->{only})   != 0;
    return 1;
  };

  my %dirs;

  if (my $old = $self->_old) {
    for my $rel (grep {; $wanted->($_) } keys %$old) {
      my $path = $self->root->child($rel);
      next unless -e $path or -l $path;
      $path->remove;
      $self->stats->{pruned}++;
      $self->_changed($rel);
      for (my $dir = $path->parent; $self->root->subsumes($dir) && "$dir" ne $self->root; $dir = $dir->parent) {
        $dirs{"$dir"} = 1;
      }
    }
  } else {
    my $iter = $self->root->iterator({ recurse => 1 });
    while (my $path = $iter->()) {
      if ($path->is_dir) {
        $dirs{"$path"} = 1;
        next;
      }

      my $rel = $path->relative($self->root)->stringify;
      next unless $wanted->($rel);

      $path->remove;
      $self->stats->{pruned}++;
      $self->_changed($rel);
    }
  }

  # Deepest first, so a parent is examined after its children are gone.
  for my $dir (sort { length $b <=> length $a } keys %dirs) {
    rmdir $dir;  # fails harmlessly unless empty
  }

  return;
}

=method save_manifest

This records what this build produced, for the next one.  Call it after the
final C<prune>.

=cut

sub save_manifest ($self) {
  my $file = $self->manifest or return;
  $file->parent->mkpath;
  $file->spew_raw($JSON->encode({ version => 1, files => $self->_produced }));
  $self->_marker->remove;
  return;
}

1;
