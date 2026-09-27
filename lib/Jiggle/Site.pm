package Jiggle::Site;
use v5.36;

use Moo;

use Encode ();
use HTML::Entities ();
use JSON::MaybeXS ();
use List::Util ();
use Jiggle::Derive;
use Jiggle::Geo qw( in_private_zone );
use Jiggle::Site::Writer;
use Mojo::ByteStream ();
use Mojo::Template;
use Path::Tiny ();

=head1 NAME

Jiggle::Site - render a library into a static website

=head1 SYNOPSIS

  my $site = Jiggle::Site->new({ library => $library });
  $site->build;

=head1 DESCRIPTION

The whole site is rendered on every build.  That's cheap for HTML, and the
expensive part (renditions) is done ahead of time by L<Jiggle::Derive> and
only hardlinked here.  L<Jiggle::Site::Writer> makes sure that files whose
content didn't change aren't touched, so an incremental sync stays small.

Private photos are treated as though they don't exist.  They're dropped when
the photo list is first assembled, so no page, count, cover, map point, or
feed can mention one.

=cut

has library => (is => 'ro', required => 1);

has out_dir => (
  is   => 'lazy',
  coerce => sub ($d) { Path::Tiny::path($d)->absolute },
  default => sub ($self) { $self->library->root->child('site') },
);

has share_dir => (
  is => 'lazy',
  default => sub { Path::Tiny::path(__FILE__)->absolute->parent(3)->child('share') },
);

has logger => (is => 'ro', default => sub { sub { } });

has config => (is => 'lazy', default => sub ($self) { $self->library->config });

sub site_title ($self) { $self->config->{title}    // 'Photos' }
sub base_url   ($self) { $self->config->{base_url} // ''       }

#---------------------------------------------------------------------------
# The model: everything the templates need, with private photos removed.

has photos => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    # Newest first.  Photos with no date sort last, by id, so the order is at
    # least stable.
    return [
      sort {;
           (defined $b->taken <=> defined $a->taken)
        || (($b->taken // '') cmp ($a->taken // ''))
        || ($a->id cmp $b->id)
      }
      grep {; $_->is_public } $self->library->photos
    ];
  },
);

has _photo_by_id => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) { return { map {; $_->id => $_ } $self->photos->@* } },
);

=method public_location

  my $loc = $site->public_location($photo);

This returns the photo's location as it may be published, or undef.  A photo
taken inside any private zone in the library's configuration has no public
location, though its metadata keeps the true one.

=cut

sub public_location ($self, $photo) {
  my $loc = $photo->location;
  return unless $loc;
  return if in_private_zone($loc, $self->config->{private_zone} // []);
  return $loc;
}

has albums => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my @albums;

    for my $album ($self->library->albums) {
      my @photos = grep {; defined } map {; $self->_photo_by_id->{$_} }
                   $album->photos->@*;
      next unless @photos;

      # The cover might be unset, or might name a private photo.
      my $cover = $self->_photo_by_id->{ $album->cover // '' } // $photos[0];

      push @albums, {
        slug   => $album->slug,
        title  => $album->title,
        description => $album->description,
        cover  => $cover,
        photos => \@photos,
      };
    }

    return \@albums;
  },
);

has _albums_for_photo => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my %for;
    for my $album ($self->albums->@*) {
      push $for{ $_->id }->@*, $album for $album->{photos}->@*;
    }
    return \%for;
  },
);

sub albums_for ($self, $photo) { $self->_albums_for_photo->{ $photo->id } // [] }

sub tag_slug ($self, $tag) {
  my $slug = lc $tag;
  $slug =~ s/[^\p{Alnum}]+/-/g;
  $slug =~ s/\A-+|-+\z//g;
  return length $slug ? $slug : '-';
}

has tags => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my %tag;

    for my $photo ($self->photos->@*) {
      for my $name ($photo->tags->@*) {
        my $slug = $self->tag_slug($name);
        $tag{$slug} //= { slug => $slug, name => $name, photos => [] };
        push $tag{$slug}{photos}->@*, $photo;
      }
    }

    return [ sort {; $a->{slug} cmp $b->{slug} } values %tag ];
  },
);

my @MONTHS = qw(
  January February March April May June July
  August September October November December
);

=method archive

This returns the photos grouped by the date they were taken:

  {
    years   => [ { year => 2026, count => 38, months => [ ... ] }, ... ],
    undated => [ @photos ],
  }

Years are newest first, and so are the months within each year.  Each month
is C<< { year, month, photos } >>, with its photos oldest first, so that a
trip reads in order.

Photos are filed by the date on the local clock where they were taken, which
is how a person remembers them: a photo taken at 23:30 on 31 July in Vienna
belongs to July, even though it was already August in UTC.

=cut

has archive => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) {
    my (%by_month, @undated);

    # $self->photos is newest first, so reversing it gives oldest first
    # within each month.
    for my $photo (reverse $self->photos->@*) {
      my ($y, $m) = ($photo->taken // '') =~ /\A(\d{4})-(\d\d)-/;
      if ($y) { push $by_month{$y}{$m}->@*, $photo }
      else    { unshift @undated, $photo }
    }

    my @years = map {;
      my $y = $_;
      my @months = map {;
        { year => $y, month => $_, photos => $by_month{$y}{$_} }
      } sort { $b cmp $a } keys $by_month{$y}->%*;

      {
        year   => $y,
        count  => List::Util::sum(map {; scalar $_->{photos}->@* } @months),
        months => \@months,
      };
    } sort { $b cmp $a } keys %by_month;

    return { years => \@years, undated => \@undated };
  },
);

sub _all_months ($self) {
  map {; $_->{months}->@* } $self->archive->{years}->@*;
}

=method sample

  my @few = $site->sample($n, @photos);

This returns up to C<$n> of the given photos, spread evenly through the list
and kept in order, for showing a preview of a year or month.

=cut

sub sample ($self, $n, @photos) {
  return @photos if @photos <= $n;
  return map {; $photos[ int($_ * @photos / $n) ] } 0 .. $n - 1;
}

#---------------------------------------------------------------------------
# Helpers for templates.

sub year_url  ($self, $year)        { "/$year/" }
sub month_url ($self, $year, $mon)  { "/$year/$mon/" }

sub month_name ($self, $mon) { $MONTHS[$mon - 1] }

# The archive month a photo belongs to, as [ year, month ], or nothing.
sub month_of ($self, $photo) {
  my ($y, $m) = ($photo->taken // '') =~ /\A(\d{4})-(\d\d)-/;
  return $y ? [ $y, $m ] : undef;
}

sub photo_url ($self, $photo) { '/p/' . $photo->id . '/' }

sub rendition_url ($self, $photo, $name) { '/p/' . $photo->id . "/$name" }

sub absolute_url ($self, $path) { $self->base_url . $path }

sub srcset ($self, $photo) {
  join q{, }, map {;
    my ($w) = Jiggle::Derive->rendition_size($photo, $_);
    $self->rendition_url($photo, $_) . " ${w}w";
  } qw( 500.webp 1024.webp 2048.webp );
}

sub rendition_size ($self, $photo, $name) {
  Jiggle::Derive->rendition_size($photo, $name);
}

sub display_date ($self, $photo) {
  my $taken = $photo->taken // return '';
  my ($y, $m, $d, $hm) = $taken =~ /\A(\d{4})-(\d\d)-(\d\d)T(\d\d:\d\d)/;
  return $taken unless $y;
  return sprintf '%d %s %d, %s', $d, $MONTHS[$m - 1], $y, $hm;
}

sub display_title ($self, $photo) {
  return $photo->title if length $photo->title;
  return $self->display_date($photo) || 'Untitled';
}

sub description_html ($self, $text) {
  return Mojo::ByteStream->new('') unless defined $text and length $text;

  # Descriptions are plain text for now: blank lines separate paragraphs.
  # Flickr descriptions allow some HTML, so imported ones may want more.
  my @paras = split /\n\s*\n/, $text;
  my $html = join qq{\n}, map {;
    my $p = HTML::Entities::encode_entities($_, q{<>&"'});
    $p =~ s{\n}{<br>\n}g;
    "<p>$p</p>";
  } @paras;

  return Mojo::ByteStream->new($html);
}

sub excerpt ($self, $text, $max = 200) {
  $text //= '';
  $text =~ s/\s+/ /g;
  $text =~ s/\A | \z//g;
  return $text if length $text <= $max;
  return substr($text, 0, $max - 1) =~ s/\s+\S*\z//r . "\x{2026}";
}

#---------------------------------------------------------------------------
# Rendering.

has _mt_cache => (is => 'ro', init_arg => undef, default => sub { {} });

sub render ($self, $template, $vars = {}) {
  my $file = $self->share_dir->child('templates', "$template.html.mt");

  my $mt = $self->_mt_cache->{$template} //= Mojo::Template->new(
    vars        => 1,
    auto_escape => 1,
    name        => "$file",
  )->parse($file->slurp_utf8);

  my $out = $mt->process({ site => $self, %$vars });
  die $out if ref $out;  # a Mojo::Exception
  return $out;
}

sub partial ($self, $template, $vars) {
  Mojo::ByteStream->new($self->render($template, $vars));
}

# Mojo::Template declares a template's variables from the keys passed on its
# first use, so every call to the layout must pass the same keys, even when
# their values are undef. -- claude, 2026-09-26
sub render_page ($self, $template, $vars) {
  my $content = $self->render($template, $vars);
  return $self->render('layout', {
    title   => $vars->{title},
    content => Mojo::ByteStream->new($content),
    og      => ($vars->{photo} ? $self->opengraph($vars->{photo}) : undef),
    map     => ($template eq 'map' || $vars->{location}) ? 1 : 0,
  });
}

sub opengraph ($self, $photo) {
  my ($w, $h) = $self->rendition_size($photo, 'og.jpg');
  return {
    title  => $self->display_title($photo),
    url    => $self->absolute_url($self->photo_url($photo)),
    image  => $self->absolute_url($self->rendition_url($photo, 'og.jpg')),
    width  => $w,
    height => $h,
    description => $self->excerpt($photo->description),
    video  => ($photo->is_video ? $self->_opengraph_video($photo) : undef),
  };
}

sub _opengraph_video ($self, $photo) {
  my ($w, $h) = $self->rendition_size($photo, 'video.mp4');
  return {
    url    => $self->absolute_url($self->rendition_url($photo, 'video.mp4')),
    width  => $w,
    height => $h,
  };
}

has writer => (
  is => 'lazy',
  init_arg => undef,
  default  => sub ($self) { Jiggle::Site::Writer->new({ root => $self->out_dir }) },
);

sub _write_page ($self, $rel, $template, $vars) {
  my $html = $self->render_page($template, $vars);
  $self->writer->write_file($rel, Encode::encode('UTF-8', $html));
}

my $JSON = JSON::MaybeXS->new->canonical->utf8;

=method build

This renders the entire site into the output directory, then removes
anything left there from earlier builds that this one didn't produce.

The renditions must already exist; see L<Jiggle::Derive>.

=cut

sub build ($self) {
  my $w = $self->writer;
  my @photos = $self->photos->@*;

  $self->_write_page('index.html', 'index', {
    title  => $self->site_title,
    photos => [ List::Util::head(100, @photos) ],
  });

  for my $i (keys @photos) {
    my $photo = $photos[$i];
    my $id    = $photo->id;

    $self->_write_page("p/$id/index.html", 'photo', {
      title    => $self->display_title($photo),
      photo    => $photo,
      location => scalar $self->public_location($photo),
      newer    => ($i > 0 ? $photos[$i - 1] : undef),
      older    => $photos[$i + 1],
      albums   => $self->albums_for($photo),
    });

    for my $recipe (Jiggle::Derive->published_recipes_for($photo)) {
      my $source = $self->library->derived_path($id, $recipe->{name});
      die "missing rendition $source\n" unless -e $source;
      $w->link_file("p/$id/$recipe->{name}", $source);
    }
  }

  $self->_write_page('albums/index.html', 'albums', {
    title  => 'Albums',
    albums => $self->albums,
  });

  for my $album ($self->albums->@*) {
    $self->_write_page("albums/$album->{slug}/index.html", 'album', {
      title => $album->{title},
      album => $album,
    });
  }

  $self->_write_page('tags/index.html', 'tags', {
    title => 'Tags',
    tags  => $self->tags,
  });

  for my $tag ($self->tags->@*) {
    $self->_write_page("tags/$tag->{slug}/index.html", 'tag', {
      title => $tag->{name},
      tag   => $tag,
    });
  }

  $self->_build_archive;

  $self->_write_page('map/index.html', 'map', { title => 'Map' });
  $w->write_file('map/photos.geojson', $JSON->encode($self->_geojson));

  $self->_copy_static;

  $w->prune;

  my $s = $w->stats;
  $self->logger->(sprintf
    '%d public photo(s); %d file(s) written, %d unchanged, %d linked, %d pruned',
    0 + @photos, @$s{qw( written unchanged linked pruned )},
  );

  return;
}

sub _build_archive ($self) {
  my $archive = $self->archive;
  my @years   = $archive->{years}->@*;

  $self->_write_page('archive/index.html', 'archive', {
    title   => 'Archive',
    years   => \@years,
    undated => $archive->{undated},
  });

  if ($archive->{undated}->@*) {
    $self->_write_page('archive/undated/index.html', 'undated', {
      title  => 'Undated',
      photos => $archive->{undated},
    });
  }

  # Years and months are newest first, so the "newer" neighbor of each is the
  # one before it in the list.
  for my $i (keys @years) {
    my $year = $years[$i];
    $self->_write_page("$year->{year}/index.html", 'year', {
      title => $year->{year},
      year  => $year,
      newer => ($i > 0 ? $years[$i - 1] : undef),
      older => $years[$i + 1],
    });
  }

  my @months = $self->_all_months;
  for my $i (keys @months) {
    my $month = $months[$i];
    $self->_write_page("$month->{year}/$month->{month}/index.html", 'month', {
      title => $self->month_name($month->{month}) . " $month->{year}",
      month => $month,
      newer => ($i > 0 ? $months[$i - 1] : undef),
      older => $months[$i + 1],
    });
  }

  return;
}

sub _geojson ($self) {
  my @features;

  for my $photo ($self->photos->@*) {
    my $loc = $self->public_location($photo) or next;
    push @features, {
      type     => 'Feature',
      geometry => { type => 'Point', coordinates => [ @$loc{qw( lon lat )} ] },
      properties => {
        title => $self->display_title($photo),
        url   => $self->photo_url($photo),
        thumb => $self->rendition_url($photo, 'h480.webp'),
      },
    };
  }

  return { type => 'FeatureCollection', features => \@features };
}

sub _copy_static ($self) {
  my $static = $self->share_dir->child('static');
  my $iter = $static->iterator({ recurse => 1 });

  while (my $file = $iter->()) {
    next if $file->is_dir or $file->basename =~ /\A\./;
    my $rel = $file->relative($static);
    $self->writer->write_file("static/$rel", $file->slurp_raw);
  }
}

1;
