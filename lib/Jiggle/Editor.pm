package Jiggle::Editor;
use v5.36;

use Moo;

use Digest::SHA ();
use Jiggle ();
use Jiggle::Derive;
use Jiggle::Album;
use Jiggle::Library;
use Jiggle::Photo;
use Jiggle::Query;
use Jiggle::TOML qw( datetime_with_offset );
use Mojo::JSON ();
use Mojo::URL;
use Mojo::Server::Daemon;
use Mojolicious;
use Path::Tiny ();

=head1 NAME

Jiggle::Editor - a local web app for reviewing and editing a batch of photos

=head1 SYNOPSIS

  my $editor = Jiggle::Editor->new({ library => $library, query => [ 'pending' ] });
  my $app    = $editor->app;     # a Mojolicious app
  say $editor->token;            # needed once, in the URL: /?token=...

=head1 DESCRIPTION

The editor serves a contact sheet of a batch of photos, chosen by a query
(see L<Jiggle::Query>) typed into the page, and a list of the library's
albums.  The batch belongs to the page: the server answers a query with a
list of photos, and the page keeps that list until it asks again, so photos
don't leave the batch as they're edited.  Everything the server sends is
read from the library's files at the time of the request, so photos added
from the command line while the editor is open are found by the next query.
OLD

Any web page in the browser can send requests to localhost, so every request
must carry the editor's token.  The first request gives it in the URL, and
gets it back as a cookie marked C<SameSite=Strict>, which the browser sends
only with requests from the editor's own pages.

=cut

has library => (is => 'ro', required => 1);

# The query the page opens on, as a list of terms; with none, it opens on the
# album list.
has query => (is => 'ro', default => sub { [] });

has token => (
  is => 'lazy',
  default => sub {
    open my $fh, '<:raw', '/dev/urandom' or die "can't read /dev/urandom: $!";
    read($fh, my $bytes, 16) == 16 or die "short read from /dev/urandom\n";
    return unpack 'H*', $bytes;
  },
);

# What remakes renditions when a write changes a photo's rotation, so the
# editor shows it turned.
has derive => (
  is => 'lazy',
  default => sub ($self) { Jiggle::Derive->new({ library => $self->library }) },
);

has share_dir => (
  is => 'ro',
  default => sub { Jiggle->share_dir },
);

my $COOKIE = 'jiggle_editor';

# The renditions the editor shows.  Others (like og.jpg) aren't needed, and
# the list keeps a request from naming arbitrary files.
my %SERVABLE = map {; $_ => 1 } qw( h480.webp 500.webp 1024.webp 2048.webp poster.png video.mp4 );

=method serve_query

  Jiggle::Editor->serve_query($library, [ 'pending' ], { port => 3001, open => 1 });

This serves an editor on 127.0.0.1 until interrupted, printing its URL (with
the token) and, if C<open> is true, opening it in a browser.  The page opens
on the given query, or, given none, on the album list.  It dies if the query
isn't one, before serving anything.

=cut

sub serve_query ($class, $library, $terms, $arg = {}) {
  my $count;
  if (@$terms) {
    my @photos = Jiggle::Query->new({ library => $library, terms => $terms })->photos;
    $count = @photos;
  }

  my $editor = $class->new({ library => $library, query => $terms });

  my $listen = "http://127.0.0.1:" . ($arg->{port} // 3001);
  my $url    = Mojo::URL->new("$listen/")->query(token => $editor->token, (@$terms ? (q => "@$terms") : ()));

  my $daemon = Mojo::Server::Daemon->new(app => $editor->app, listen => [ $listen ], silent => 1);
  $daemon->start;

  say defined $count ? sprintf('%d photo(s); editing at %s', $count, $url) : "editing at $url";
  say 'press control-C to stop';
  system('open', $url) if $^O eq 'darwin' && $arg->{open};

  $daemon->ioloop->start;
}

sub app ($self) {
  my $app = Mojolicious->new;
  $app->log->level('warn');
  $app->secrets([ $self->token ]);
  $app->types->type(webp => 'image/webp');

  my $editor_dir = $self->share_dir->child('editor');

  my $r = $app->routes;

  $r->get('/' => sub ($c) {
    if (defined(my $token = $c->param('token'))) {
      return $c->render(text => 'bad token', status => 403) unless $token eq $self->token;
      $c->cookie($COOKIE => $token, { httponly => 1, samesite => 'Strict', path => '/' });
      my $q = $c->param('q');
      return $c->redirect_to(defined $q ? $c->url_for('/')->query(q => $q) : '/');
    }
    return $c->render(text => 'no token', status => 403)
      unless ($c->cookie($COOKIE) // '') eq $self->token;

    $c->res->headers->content_type('text/html; charset=utf-8');
    $c->res->headers->cache_control('no-cache');
    $c->render(data => $editor_dir->child('index.html')->slurp_raw);
  });

  my $authed = $r->under('/' => sub ($c) {
    return 1 if ($c->cookie($COOKIE) // '') eq $self->token;
    $c->render(json => { error => 'no token' }, status => 403);
    return undef;
  });

  $authed->get('/static/*file' => sub ($c) {
    my $file = $c->param('file');
    return $c->reply->not_found if $file =~ m{(?:\A|/)\.};
    my $path = $editor_dir->child('static', $file);
    return $c->reply->not_found unless -f $path;
    $c->res->headers->cache_control('no-cache');
    $c->reply->file("$path");
  });

  $authed->get('/api/batch' => sub ($c) {
    $c->res->headers->cache_control('no-store');
    my $ids = $c->param('ids');
    my $data = eval {
      $self->batch_data(defined $ids ? { ids => [ split /,/, $ids ] } : { q => $c->param('q') // '' });
    };
    return $c->render(json => { error => $@ =~ s/\n\z//r }, status => 400) unless $data;
    $c->render(json => $data);
  });

  $authed->get('/api/albums' => sub ($c) {
    $c->res->headers->cache_control('no-store');
    $c->render(json => { albums => $self->album_overview });
  });

  $authed->post('/api/write' => sub ($c) {
    my $result = $self->write_changes($c->req->json // {});
    $c->render(json => $result, status => $result->{status} // 200);
  });

  $authed->get('/r/:id/#name' => sub ($c) {
    my ($id, $name) = ($c->param('id'), $c->param('name'));
    return $c->reply->not_found unless $id =~ /\A[0-9a-z]+\z/ and $SERVABLE{$name};
    my $path = $self->library->derived_path($id, $name);
    return $c->reply->not_found unless -f $path;
    $c->res->headers->cache_control('no-cache');
    $c->reply->file("$path");
  });

  return $app;
}

=method batch_data

  my $data = $editor->batch_data({ q => 'pending limit:50' });
  my $data = $editor->batch_data({ ids => [ @ids ] });

This returns a batch as the editor's page gets it: the photos a query picks
(its terms separated by spaces), or the given photos, in order, as they are
on disk now; the library's albums; and every tag in the library, for
suggestions.  Each photo has a C<version>, the digest of its metadata file,
which a write must present so that changes made to the file since it was
loaded aren't overwritten.  It dies if the query isn't one.

If the query is exactly one C<album:SLUG> term, the batch is that album, and
the album can be edited, too, so there's an C<album>: its C<slug>, C<title>,
C<description>, C<cover>, C<photos> (in order), and C<version>, the digest of
its file.  With any other term beside it, the batch is only part of the
album, so the album isn't offered for editing.

=cut

sub batch_data ($self, $arg) {
  # The library is read afresh, so photos added since the last request are
  # found.  With its cache of parsed metadata, that's well under a second
  # for twelve thousand photos.  -- claude, 2026-10-03
  my $library = Jiggle::Library->new({ root => $self->library->root });

  my (@ids, $album, $query);
  if ($arg->{ids}) {
    @ids = grep {; -e $library->meta_path($_) } $arg->{ids}->@*;
  } else {
    my @terms = split ' ', $arg->{q} // '';
    $query = "@terms";
    @ids = map {; $_->id } Jiggle::Query->new({ library => $library, terms => \@terms })->photos;
    ($album) = @terms == 1 ? $terms[0] =~ /\Aalbum:(.+)\z/ : ();
  }

  my @albums    = $self->_albums;
  my %albums_of = $self->_albums_of(@albums);
  my %tags = map {; $_ => 1 } map {; $_->tags->@* } $library->photos;

  return {
    (defined $query ? (query => $query) : ()),
    photos => [ map {; $self->_photo_record($_, \%albums_of) } @ids ],
    tags   => [ sort { fc $a cmp fc $b } keys %tags ],
    albums => _album_list(@albums),
    (defined $album ? (album => $self->_album_record($album)) : ()),
  };
}

=method album_overview

This returns every album, for the editor's album list, sorted by title: its
C<slug>, C<title>, C<created>, C<cover> (or its first photo), and how many of
its photos are C<published>, C<pending> (whatever their visibility), and
C<private> (and not pending).  Albums with nothing published, which the site
leaves out, are included.

=cut

sub album_overview ($self) {
  my $library = Jiggle::Library->new({ root => $self->library->root });

  my @overview;
  for my $album ($self->_albums) {
    my @photos = grep {; defined } map {; $library->photo($_) } $album->photos->@*;
    push @overview, {
      slug    => $album->slug,
      title   => $album->title,
      created => $album->created,
      cover   => $album->cover // ($photos[0] && $photos[0]->id),
      published => scalar(grep {; $_->is_published } @photos),
      pending   => scalar(grep {; $_->pending } @photos),
      private   => scalar(grep {; ! $_->is_public and ! $_->pending } @photos),
    };
  }

  return [ sort {; fc $a->{title} cmp fc $b->{title} } @overview ];
}

sub _album_file ($self, $slug) { $self->library->albums_dir->child("$slug.toml") }

# An album, as the page gets it for editing, read from its file now; undef
# if it's gone.
sub _album_record ($self, $slug) {
  my $file = $self->_album_file($slug);
  return undef unless -e $file;

  my $bytes = $file->slurp_raw;
  my $album = Jiggle::Album->from_toml_file($file);
  return {
    slug        => $album->slug,
    title       => $album->title,
    description => $album->description,
    cover       => $album->cover,
    photos      => [ $album->photos->@* ],
    version     => Digest::SHA::sha1_hex($bytes),
  };
}

=method photo_fields

This returns a photo's editable fields, and the facts shown beside them, as a
hash reference ready for JSON.

=cut

sub photo_fields ($self, $photo) {
  my $bool = sub ($v) { $v ? Mojo::JSON::true : Mojo::JSON::false };
  my $loc  = $photo->location;

  return {
    id          => $photo->id,
    type        => $photo->type,
    title       => $photo->title,
    description => $photo->description,
    tags        => [ $photo->tags->@* ],
    visibility  => $photo->visibility,
    pending     => $bool->($photo->pending),
    taken       => $photo->taken,
    rotate      => 0 + $photo->rotate,
    location    => $loc ? { private => $bool->($loc->{private}) } : undef,

    width       => 0 + $photo->width,
    height      => 0 + $photo->height,
    file        => $photo->original->{file},
    duration    => $photo->duration,
    added       => $photo->added_at,
    flickr_id   => $photo->flickr_id,
  };
}

=method write_changes

  my $result = $editor->write_changes({
    note   => 'optional; goes in the commit message',
    photos => [
      { id => $id, version => $version, changes => { title => '...', ... } },
      ...
    ],
  });

This writes the given changes to the photos' metadata files, and commits the
files it changed to F<meta>'s git repository.  It's all or nothing: nothing
is written unless every change is valid and every photo's C<version> matches
its file as it is now, so a file changed since the editor loaded it (by hand,
or a C<git pull>) is never overwritten.

The changes are to the fields L</photo_fields> gives, except that
C<location> is changed as C<location_private>, a boolean.  An empty C<taken>
removes it.

C<albums> is changed as C<< { add => [ ... ], remove => [ ... ] } >>, naming
albums by slug, or by the key of a new album the request makes:

  new_albums => [ { key => 'new-1', title => 'Berlin, 2026-07' } ]

Additions go at the end of the album, and are merged with the album's file as
it is now.  An album whose cover is removed gets its first photo as its cover.

A photo whose rotation changes has its renditions remade before this returns.

When editing an album, the request names it, and can change it, too:

  album => {
    slug    => $slug,
    version => $version,
    changes => { title => '...', description => '...', cover => $id, order => [ @ids ] },
  }

If it changes anything, its C<version> is checked like a photo's.  C<order> must name the album's
photos, each once.  A photo removed from the album in the same request is
left out of the order, and C<cover> must be one of the photos the album has
afterward.

The result has C<photos>, each written photo as L</batch_data> would give it,
and C<commit>, the new commit's abbreviated id (undef if nothing changed, or
F<meta> isn't a git repository).  It also has C<albums>, every album, and
C<new_albums>, which maps each new album's key to its slug, and, if the
request named an album, C<album>, as L</batch_data> gives it.  A failure has a C<status>
(409 for a conflict, 400 for anything else) and an C<error>, and a conflict
lists the photos in C<conflicts>, and has C<album_conflict> true if the
album's file had changed.

=cut

my %TEXT_FIELD = map {; $_ => 1 } qw( title description );

# A TOML datetime, local or with an offset, to the second.
my $DATETIME = qr/\A[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:Z|[-+][0-9]{2}:[0-9]{2})?\z/;

sub write_changes ($self, $request) {
  my (@plan, @conflicts, %fields_changed, %touched);

  # Albums as they are on disk now, and any the request makes.  Membership
  # changes are applied to these, as additions and removals, so a change
  # made to an album file since the page loaded is kept, not overwritten.
  my %album = map {; $_->slug => $_ } $self->_albums;
  my %before = map {; $_ => $album{$_}->as_toml } keys %album;
  my %slug_for_key;
  my %taken = map {; $_ => 1 } keys %album;
  for my $spec (($request->{new_albums} // [])->@*) {
    my ($key, $title) = ($spec->{key} // '', $spec->{title} // '');
    $title =~ s/\A\s+|\s+\z//g;
    return _failure(400, 'a new album needs a key and a title') unless length $key and length $title;
    my $slug = Jiggle::Album->unique_slug($title, \%taken);
    $slug_for_key{$key} = $slug;
    $album{$slug} = Jiggle::Album->new({
      slug    => $slug,
      title   => $title,
      created => datetime_with_offset(time),
    });
  }

  # Changes to the album being edited, checked now and applied with the
  # membership changes below.
  my $edit = $request->{album};
  my $editing = $edit && $edit->{slug};
  return _failure(400, "no album $editing") if defined $editing and ! $album{$editing};

  my (%album_change, $album_conflict);
  if ($editing and my %changes = ($edit->{changes} // {})->%*) {
    my $file = $self->_album_file($editing);
    if (Digest::SHA::sha1_hex($file->slurp_raw) ne ($edit->{version} // '')) {
      $album_conflict = 1;
    } else {
      for my $field (sort keys %changes) {
        my $error = _album_change(\%album_change, $album{$editing}, $field, $changes{$field});
        return _failure(400, "album: $error") if $error;
      }
    }
  }

  my (%add_to, %remove_from);
  for my $item (($request->{photos} // [])->@*) {
    my $id = $item->{id} // '';
    my $file = $id =~ /\A[0-9a-z]+\z/ && $self->library->meta_path($id);
    return _failure(400, "no photo $id") unless $file and -e $file;

    my $bytes = $file->slurp_raw;
    if (Digest::SHA::sha1_hex($bytes) ne ($item->{version} // '')) {
      push @conflicts, $id;
      next;
    }

    my $photo = Jiggle::Photo->from_toml_file($file);
    my %attr  = %$photo;

    my %changes = ($item->{changes} // {})->%*;
    if (my $albums = delete $changes{albums}) {
      return _failure(400, "$id: albums must be { add, remove }") unless ref $albums eq 'HASH';
      for my $op (qw( add remove )) {
        my $ops = $op eq 'add' ? \%add_to : \%remove_from;
        for my $name (($albums->{$op} // [])->@*) {
          my $slug = $slug_for_key{$name} // $name;
          return _failure(400, "$id: no album $name") unless $album{$slug};
          push $ops->{$slug}->@*, $id;
        }
      }
      $fields_changed{albums} = 1;
      $touched{$id} = 1;
    }

    for my $field (sort keys %changes) {
      my $error = $self->_apply_change(\%attr, $field, $changes{$field});
      return _failure(400, "$id: $error") if $error;
      $fields_changed{$field} = 1;
    }

    my $new = eval { Jiggle::Photo->new(\%attr) };
    return _failure(400, "$id: " . ($@ =~ s/ at .*//sr)) unless $new;

    if ($new->as_toml ne $bytes) {
      push @plan, { file => $file, text => $new->as_toml, turned => $new->rotate != $photo->rotate, photo => $new };
      $touched{$id} = 1;
    }
  }

  if (@conflicts or $album_conflict) {
    return _failure(409,
      'changed on disk since loading: ' . join(q{, }, @conflicts, ($album_conflict ? 'the album' : ())),
      conflicts      => \@conflicts,
      album_conflict => $album_conflict ? Mojo::JSON::true : Mojo::JSON::false,
    );
  }

  my (@new_titles, $album_edited);
  for my $slug (sort keys %album) {
    my $old   = $album{$slug};
    my $edits = $slug eq ($editing // '') ? \%album_change : {};

    my %gone = map {; $_ => 1 } ($remove_from{$slug} // [])->@*;
    my @photos = grep {; ! $gone{$_} } ($edits->{order} // $old->photos)->@*;
    my %have = map {; $_ => 1 } @photos;
    push @photos, grep {; ! $have{$_}++ } ($add_to{$slug} // [])->@*;

    # A new album with nothing in it was made and then abandoned.
    next if ! $before{$slug} and ! @photos;

    my $cover = $old->cover;
    if (exists $edits->{cover}) {
      $cover = $edits->{cover};
      return _failure(400, "album: the cover, $cover, isn't one of its photos")
        unless grep {; $_ eq $cover } @photos;
    }
    $cover = $photos[0] unless defined $cover and grep {; $_ eq $cover } @photos;

    my %arg = (%$old, photos => \@photos);
    $arg{$_} = $edits->{$_} for grep {; exists $edits->{$_} } qw( title description );
    delete $arg{cover};
    $arg{cover} = $cover if defined $cover;
    my $new = Jiggle::Album->new(\%arg);

    my $text = $new->as_toml;
    next if defined $before{$slug} and $text eq $before{$slug};

    $album_edited = 1 if %$edits;
    push @new_titles, $new->title unless $before{$slug};
    push @plan, { file => $self->library->albums_dir->child("$slug.toml"), text => $text };
  }

  $self->library->albums_dir->mkpath if @plan;
  for my $step (@plan) {
    my $tmp = $step->{file}->sibling('.' . $step->{file}->basename . '.tmp');
    $tmp->spew_utf8($step->{text});
    $tmp->move($step->{file});
  }

  my $commit;
  if (@plan) {
    my @what;
    push @what, sprintf 'edit %d photo(s): %s', scalar keys %touched, join q{, }, sort keys %fields_changed
      if %touched;
    push @what, sprintf 'edit album %s: %s', $editing, join q{, }, sort keys %album_change
      if $album_edited;
    my $message = join '; ', @what;
    $message .= '; new album: ' . join q{, }, @new_titles if @new_titles;
    $message .= "\n\n$request->{note}" if ($request->{note} // '') =~ /\S/;
    $commit = $self->library->commit_meta($message, map {; $_->{file} } @plan);
  }

  # The write is done (and committed) whether or not this works; a photo
  # whose renditions fail is noted in derive's manifest, as always.
  my @turned = map {; $_->{photo} } grep {; $_->{turned} } @plan;
  $self->derive->derive_photos(@turned) if @turned;

  my @albums    = $self->_albums;
  my %albums_of = $self->_albums_of(@albums);
  return {
    commit => $commit,
    photos => [
      map  {; $self->_photo_record($_, \%albums_of) }
      grep {; $touched{$_} } map {; $_->{id} } ($request->{photos} // [])->@*
    ],
    albums     => _album_list(@albums),
    new_albums => \%slug_for_key,
    ($editing ? (album => $self->_album_record($editing)) : ()),
  };
}

# Checks one change to the album being edited, and records it, returning an
# error, if any.
sub _album_change ($change, $album, $field, $value) {
  return "$field must be a string" if $field ne 'order' and (ref $value or ! defined $value);

  if ($field eq 'title') {
    $value =~ s/\A\s+|\s+\z//g;
    return 'the title must not be empty' unless length $value;
  }
  elsif ($field eq 'order') {
    return 'order must be a list of ids' unless ref $value eq 'ARRAY' and ! grep {; ref or ! defined } @$value;
    return "order must name the album's photos, each once"
      unless join("\0", sort @$value) eq join("\0", sort $album->photos->@*);
  }
  elsif ($field ne 'description' and $field ne 'cover') {
    return "can't change $field";
  }

  $change->{$field} = $value;
  return;
}

sub _album_list (@albums) {
  return [
    sort {; fc $a->{title} cmp fc $b->{title} }
    map  {; { slug => $_->slug, title => $_->title } } @albums
  ];
}

sub _failure ($status, $error, %more) {
  return { status => $status, error => $error, %more };
}

# Applies one change to a photo's attributes, returning an error, if any.
sub _apply_change ($self, $attr, $field, $value) {
  if ($TEXT_FIELD{$field}) {
    return "$field must be a string" if ref $value or ! defined $value;
    $attr->{$field} = $value;
  }
  elsif ($field eq 'tags') {
    return 'tags must be a list' unless ref $value eq 'ARRAY';
    my (%seen, @tags);
    for my $tag (@$value) {
      return 'tags must be strings' if ref $tag or ! defined $tag;
      $tag =~ s/\A\s+|\s+\z//g;
      push @tags, $tag if length $tag and ! $seen{$tag}++;
    }
    $attr->{tags} = \@tags;
  }
  elsif ($field eq 'visibility') {
    return "unknown visibility" unless ($value // '') =~ /\A(?:public|private)\z/;
    $attr->{visibility} = $value;
  }
  elsif ($field eq 'pending') {
    $attr->{pending} = $value ? 1 : 0;
  }
  elsif ($field eq 'taken') {
    $value //= '';
    return "taken must be a datetime like 2026-09-30T10:15:00" if length $value and $value !~ $DATETIME;
    if (length $value) { $attr->{taken} = $value } else { delete $attr->{taken} }
  }
  elsif ($field eq 'rotate') {
    return 'rotate must be 0, 90, 180, or 270'
      unless defined $value and ! ref $value and $value =~ /\A(?:0|90|180|270)\z/;
    $attr->{rotate} = 0 + $value;
  }
  elsif ($field eq 'location_private') {
    return 'no location to make private' unless $attr->{location};
    my %loc = $attr->{location}->%*;
    if ($value) { $loc{private} = 1 } else { delete $loc{private} }
    $attr->{location} = \%loc;
  }
  else {
    return "can't change $field";
  }

  return;
}

# One photo, as the page gets it, read from its file now.
sub _photo_record ($self, $id, $albums_of) {
  my $file  = $self->library->meta_path($id);
  my $bytes = $file->slurp_raw;
  my $photo = Jiggle::Photo->from_toml_file($file);
  return {
    $self->photo_fields($photo)->%*,
    albums  => [ sort { $a cmp $b } ($albums_of->{$id} // [])->@* ],
    version => Digest::SHA::sha1_hex($bytes),
  };
}

# { photo id => [ album slug, ... ] }
sub _albums_of ($self, @albums) {
  @albums = $self->_albums unless @albums;
  my %albums_of;
  for my $album (@albums) {
    push $albums_of{$_}->@*, $album->slug for $album->photos->@*;
  }
  return %albums_of;
}

sub _albums ($self) {
  my $dir = $self->library->albums_dir;
  return () unless -d $dir;
  return map {; Jiggle::Album->from_toml_file($_) } sort $dir->children(qr/\.toml\z/);
}

1;
